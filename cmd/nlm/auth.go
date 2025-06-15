// chagne this to use x/term and write the auth file to the users's home dir in a cache file.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/tmc/nlm/internal/auth"
	"golang.org/x/term"
)

func handleAuth(args []string, debug bool) (string, string, error) {
	isTty := term.IsTerminal(int(os.Stdin.Fd()))

	// Force browser mode if NLM_FORCE_BROWSER is set or if we're explicitly in auth command
	forceBrowser := os.Getenv("NLM_FORCE_BROWSER") != ""

	if debug {
		fmt.Fprintf(os.Stderr, "TTY detected: %v, Force browser: %v, Args: %v\n", isTty, forceBrowser, args)
	}

	if !isTty && !forceBrowser {
		// Parse HAR/curl from stdin
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", "", fmt.Errorf("failed to read stdin: %w", err)
		}
		return detectAuthInfo(string(input))
	}

	// Get available profiles and use the first one as default
	profiles, err := listAvailableProfiles()
	if err != nil {
		return "", "", fmt.Errorf("list profiles: %w", err)
	}

	profileName := "Default"
	if len(profiles) > 0 {
		// Try to find the most recently used profile
		recentProfile := getMostRecentProfile(profiles)
		if recentProfile != "" {
			profileName = recentProfile
		} else {
			profileName = profiles[0] // Use first available profile as fallback
		}
	}

	if v := os.Getenv("NLM_BROWSER_PROFILE"); v != "" {
		profileName = v
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		if args[0] == "list-profiles" {
			return showAvailableProfiles()
		}
		profileName = args[0]
	}

	a := auth.New(debug)

	// Show which profile is being used
	var profileMessage string
	if v := os.Getenv("NLM_BROWSER_PROFILE"); v != "" {
		profileMessage = fmt.Sprintf("profile:%s (from NLM_BROWSER_PROFILE)", profileName)
	} else {
		recentProfile := getMostRecentProfile(profiles)
		if profileName == recentProfile {
			profileMessage = fmt.Sprintf("profile:%s (most recent)", profileName)
		} else {
			profileMessage = fmt.Sprintf("profile:%s", profileName)
		}
	}

	fmt.Fprintf(os.Stderr, "nlm: launching browser to login... (%s)\n", profileMessage)
	token, cookies, err := a.GetAuth(auth.WithProfileName(profileName))
	if err != nil {
		return "", "", fmt.Errorf("browser auth failed: %w", err)
	}
	return persistAuthToDisk(cookies, token, profileName)
}

func readFromStdin() (string, error) {
	var input strings.Builder
	buf := make([]byte, 1024)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}
		input.Write(buf[:n])
	}
	return input.String(), nil
}

func detectAuthInfo(cmd string) (string, string, error) {
	// Extract cookies
	cookieRe := regexp.MustCompile(`-H ['"]cookie: ([^'"]+)['"]`)
	cookieMatch := cookieRe.FindStringSubmatch(cmd)
	if len(cookieMatch) < 2 {
		return "", "", fmt.Errorf("no cookies found")
	}
	cookies := cookieMatch[1]

	// Extract auth token
	atRe := regexp.MustCompile(`at=([^&\s]+)`)
	atMatch := atRe.FindStringSubmatch(cmd)
	if len(atMatch) < 2 {
		return "", "", fmt.Errorf("no auth token found")
	}
	authToken := atMatch[1]
	persistAuthToDisk(cookies, authToken, "")
	return authToken, cookies, nil
}

func persistAuthToDisk(cookies, authToken, profileName string) (string, string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("get home dir: %w", err)
	}

	// Create .nlm directory if it doesn't exist
	nlmDir := filepath.Join(homeDir, ".nlm")
	if err := os.MkdirAll(nlmDir, 0700); err != nil {
		return "", "", fmt.Errorf("create .nlm directory: %w", err)
	}

	// Create or update env file
	envFile := filepath.Join(nlmDir, "env")
	content := fmt.Sprintf("NLM_COOKIES=%q\nNLM_AUTH_TOKEN=%q\nNLM_BROWSER_PROFILE=%q\n",
		cookies,
		authToken,
		profileName,
	)

	if err := os.WriteFile(envFile, []byte(content), 0600); err != nil {
		return "", "", fmt.Errorf("write env file: %w", err)
	}

	fmt.Fprintf(os.Stderr, "nlm: auth info written to %s\n", envFile)
	return authToken, cookies, nil
}

func loadStoredEnv() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}

	data, err := os.ReadFile(filepath.Join(home, ".nlm", "env"))
	if err != nil {
		return
	}

	s := bufio.NewScanner(strings.NewReader(string(data)))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}

		key = strings.TrimSpace(key)
		if os.Getenv(key) != "" {
			continue
		}

		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		os.Setenv(key, value)
	}
}

func showAvailableProfiles() (string, string, error) {
	profiles, err := listAvailableProfiles()
	if err != nil {
		return "", "", fmt.Errorf("list profiles: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Available Chrome profiles:\n")
	mostRecent := getMostRecentProfile(profiles)
	for i, profile := range profiles {
		info := getProfileInfo(profile)
		recentFlag := ""
		if profile == mostRecent {
			recentFlag = " [most recent]"
		}
		fmt.Fprintf(os.Stderr, "  %d. %s%s%s\n", i+1, profile, info, recentFlag)
	}

	if len(profiles) == 0 {
		fmt.Fprintf(os.Stderr, "No Chrome profiles found.\n")
	} else {
		fmt.Fprintf(os.Stderr, "\nUse: nlm auth <profile-name>\n")
		fmt.Fprintf(os.Stderr, "Example: nlm auth \"%s\"\n", profiles[0])
		fmt.Fprintf(os.Stderr, "Current default: %s\n", getCurrentDefaultProfile())
	}

	return "", "", nil
}

func getProfileInfo(profileName string) string {
	home, _ := os.UserHomeDir()
	prefsPath := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", profileName, "Preferences")

	data, err := os.ReadFile(prefsPath)
	if err != nil {
		return ""
	}

	var prefs map[string]interface{}
	if err := json.Unmarshal(data, &prefs); err != nil {
		return ""
	}

	// Try to get account information
	if account, ok := prefs["account_info"].([]interface{}); ok && len(account) > 0 {
		if accountData, ok := account[0].(map[string]interface{}); ok {
			if email, ok := accountData["email"].(string); ok {
				return fmt.Sprintf(" (%s)", email)
			}
		}
	}

	// Alternative: check profile info
	if profile, ok := prefs["profile"].(map[string]interface{}); ok {
		if name, ok := profile["name"].(string); ok && name != "" {
			return fmt.Sprintf(" (%s)", name)
		}
	}

	return ""
}

func getCurrentDefaultProfile() string {
	if v := os.Getenv("NLM_BROWSER_PROFILE"); v != "" {
		return v
	}
	return "Default"
}

func getMostRecentProfile(profiles []string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	var mostRecent string
	var mostRecentTime int64

	for _, profile := range profiles {
		// Check the modification time of the Cookies file
		cookiesPath := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", profile, "Cookies")
		if stat, err := os.Stat(cookiesPath); err == nil {
			modTime := stat.ModTime().Unix()
			if modTime > mostRecentTime {
				mostRecentTime = modTime
				mostRecent = profile
			}
		}

		// Also check the Preferences file
		prefsPath := filepath.Join(home, "Library", "Application Support", "Google", "Chrome", profile, "Preferences")
		if stat, err := os.Stat(prefsPath); err == nil {
			modTime := stat.ModTime().Unix()
			if modTime > mostRecentTime {
				mostRecentTime = modTime
				mostRecent = profile
			}
		}
	}

	return mostRecent
}

func listAvailableProfiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	profilePath := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	entries, err := os.ReadDir(profilePath)
	if err != nil {
		return nil, fmt.Errorf("read profile directory: %w", err)
	}

	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() {
			name := entry.Name()
			// Check if it's a valid profile directory
			if name == "Default" || strings.HasPrefix(name, "Profile ") {
				// Verify it has essential profile files
				cookiesPath := filepath.Join(profilePath, name, "Cookies")
				if _, err := os.Stat(cookiesPath); err == nil {
					profiles = append(profiles, name)
				}
			}
		}
	}

	return profiles, nil
}
