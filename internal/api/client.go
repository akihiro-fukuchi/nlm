// Package api provides the NotebookLM API client.
package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/davecgh/go-spew/spew"
	pb "github.com/tmc/nlm/gen/notebooklm/v1alpha1"
	"github.com/tmc/nlm/internal/batchexecute"
	"github.com/tmc/nlm/internal/beprotojson"
	"github.com/tmc/nlm/internal/rpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type (
	Notebook = pb.Project
	Note     = pb.Source
)

// Client handles NotebookLM API interactions.
type Client struct {
	rpc *rpc.Client
}

// New creates a new NotebookLM API client.
func New(authToken, cookies string, opts ...batchexecute.Option) *Client {
	return &Client{
		rpc: rpc.New(authToken, cookies, opts...),
	}
}

// Project/Notebook operations

func (c *Client) ListRecentlyViewedProjects() ([]*Notebook, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCListRecentlyViewedProjects,
		Args: []interface{}{nil, 1},
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}

	// Parse the complex nested structure from NotebookLM
	if len(resp) > 0 && resp[0] == '[' {
		var outerArray []interface{}
		if err := json.Unmarshal(resp, &outerArray); err == nil {
			if len(outerArray) > 0 {
				// Try to parse the first element as the projects array
				if projectsArray, ok := outerArray[0].([]interface{}); ok {
					return parseProjectsFromArray(projectsArray)
				}
			}
		}

		// Fallback: Check if it's just a metadata array like [16]
		if len(outerArray) == 1 {
			return []*Notebook{}, nil
		}
	}

	var response pb.ListRecentlyViewedProjectsResponse
	if err := beprotojson.Unmarshal(resp, &response); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return response.Projects, nil
}

func parseProjectsFromArray(projectsArray []interface{}) ([]*Notebook, error) {
	var notebooks []*Notebook

	for _, item := range projectsArray {
		projectData, ok := item.([]interface{})
		if !ok || len(projectData) < 5 {
			continue // Skip invalid project data
		}

		// Extract project information
		title, _ := projectData[0].(string)
		projectID, _ := projectData[2].(string)
		emoji, _ := projectData[3].(string)

		if title == "" || projectID == "" {
			continue // Skip projects without essential data
		}

		// Parse metadata (if available)
		var createTime *timestamppb.Timestamp
		if len(projectData) > 5 && projectData[5] != nil {
			if metadataArray, ok := projectData[5].([]interface{}); ok {
				// Look for timestamp in specific positions based on API structure
				// Index 5: Last updated timestamp [seconds, nanos]
				// Index 8: Creation timestamp [seconds, nanos]

				// Try index 5 first (last updated)
				if len(metadataArray) > 5 && metadataArray[5] != nil {
					if timestampArray, ok := metadataArray[5].([]interface{}); ok && len(timestampArray) >= 2 {
						if seconds, ok := timestampArray[0].(float64); ok {
							nanos := int32(0)
							if nanosFloat, ok := timestampArray[1].(float64); ok {
								nanos = int32(nanosFloat)
							}
							createTime = &timestamppb.Timestamp{
								Seconds: int64(seconds),
								Nanos:   nanos,
							}
						}
					}
				}

				// If not found at index 5, try index 8 (creation time)
				if createTime == nil && len(metadataArray) > 8 && metadataArray[8] != nil {
					if timestampArray, ok := metadataArray[8].([]interface{}); ok && len(timestampArray) >= 2 {
						if seconds, ok := timestampArray[0].(float64); ok {
							nanos := int32(0)
							if nanosFloat, ok := timestampArray[1].(float64); ok {
								nanos = int32(nanosFloat)
							}
							createTime = &timestamppb.Timestamp{
								Seconds: int64(seconds),
								Nanos:   nanos,
							}
						}
					}
				}
			}
		}

		notebook := &pb.Project{
			ProjectId: projectID,
			Title:     title,
			Emoji:     emoji,
			Metadata: &pb.ProjectMetadata{
				CreateTime: createTime,
			},
		}

		notebooks = append(notebooks, notebook)
	}

	return notebooks, nil
}

func parseProjectFromGetProjectResponse(outerArray []interface{}) *Notebook {
	if len(outerArray) == 0 {
		return nil
	}

	// The response structure is [["title", [sources], projectID, emoji, ...]]
	if projectArray, ok := outerArray[0].([]interface{}); ok && len(projectArray) >= 1 {
		title, _ := projectArray[0].(string)

		// Parse sources from the second element
		var sources []*pb.Source
		if len(projectArray) > 1 && projectArray[1] != nil {
			if sourcesArray, ok := projectArray[1].([]interface{}); ok && len(sourcesArray) > 0 {
				// Each element in sourcesArray is a source
				sources = parseSourcesFromResponseArray(sourcesArray)
			}
		}

		// Extract project ID and emoji from the response structure
		var projectID, emoji string
		if len(projectArray) > 2 {
			projectID, _ = projectArray[2].(string)
		}
		if len(projectArray) > 3 {
			emoji, _ = projectArray[3].(string)
		}

		project := &pb.Project{
			ProjectId: projectID,
			Title:     title,
			Emoji:     emoji,
			Sources:   sources,
			Metadata:  &pb.ProjectMetadata{},
		}
		return project
	}

	return nil
}

func parseSourcesFromResponseArray(sourcesData []interface{}) []*pb.Source {
	var sources []*pb.Source

	// Each element in sourcesData is a source array with structure:
	// [[sourceID], title, [metadata...], [status...]]
	for _, sourceItem := range sourcesData {
		if sourceArray, ok := sourceItem.([]interface{}); ok && len(sourceArray) >= 4 {
			// Extract source ID from the first element (ID array)
			var sourceID string
			if idArray, ok := sourceArray[0].([]interface{}); ok && len(idArray) > 0 {
				if id, ok := idArray[0].(string); ok {
					sourceID = id
				}
			}

			// Extract title from the second element
			title, _ := sourceArray[1].(string)

			// Skip if we don't have essential data
			if sourceID == "" || title == "" {
				continue
			}

			// Extract metadata from the third element
			var sourceType pb.SourceType = pb.SourceType_SOURCE_TYPE_UNSPECIFIED
			var status pb.SourceSettings_SourceStatus = pb.SourceSettings_SOURCE_STATUS_ENABLED
			var lastModifiedTime *timestamppb.Timestamp

			if metadataArray, ok := sourceArray[2].([]interface{}); ok {
				// Parse timestamp from metadata (index 2 is usually the timestamp array)
				if len(metadataArray) > 2 && metadataArray[2] != nil {
					if timestampArray, ok := metadataArray[2].([]interface{}); ok && len(timestampArray) >= 2 {
						if seconds, ok := timestampArray[0].(float64); ok {
							if nanos, ok := timestampArray[1].(float64); ok {
								lastModifiedTime = &timestamppb.Timestamp{
									Seconds: int64(seconds),
									Nanos:   int32(nanos),
								}
							}
						}
					}
				}

				// Try to determine source type from URL in metadata (index 7)
				if len(metadataArray) > 7 && metadataArray[7] != nil {
					if urlArray, ok := metadataArray[7].([]interface{}); ok && len(urlArray) > 0 {
						if url, ok := urlArray[0].(string); ok && strings.HasPrefix(url, "http") {
							sourceType = pb.SourceType_SOURCE_TYPE_WEB_PAGE
						}
					}
				}
			}

			source := &pb.Source{
				SourceId: &pb.SourceId{
					SourceId: sourceID,
				},
				Title: title,
				Metadata: &pb.SourceMetadata{
					SourceType:       sourceType,
					LastModifiedTime: lastModifiedTime,
				},
				Settings: &pb.SourceSettings{
					Status: status,
				},
			}

			sources = append(sources, source)
		}
	}

	return sources
}

func (c *Client) CreateProject(title string, emoji string) (*Notebook, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCCreateProject,
		Args: []interface{}{title, emoji},
	})
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}

	var project pb.Project
	if err := beprotojson.Unmarshal(resp, &project); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &project, nil
}

func (c *Client) GetProject(projectID string) (*Notebook, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGetProject,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}

	// Debug: Check actual response content (commented out for production)
	// fmt.Printf("DEBUG GetProject: Raw response (%d bytes): %q\n", len(resp), string(resp[:min(500, len(resp))]))

	// Try to parse the complex nested structure similar to ListRecentlyViewedProjects
	if len(resp) > 0 && resp[0] == '[' {
		var outerArray []interface{}
		if err := json.Unmarshal(resp, &outerArray); err == nil {
			if len(outerArray) > 0 {
				// Try to parse project data from the array structure
				if project := parseProjectFromGetProjectResponse(outerArray); project != nil {
					return project, nil
				}
			}
		}
	}

	var project pb.Project
	if err := beprotojson.Unmarshal(resp, &project); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &project, nil
}

func (c *Client) DeleteProjects(projectIDs []string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCDeleteProjects,
		Args: []interface{}{projectIDs},
	})
	if err != nil {
		return fmt.Errorf("delete projects: %w", err)
	}
	return nil
}

func (c *Client) MutateProject(projectID string, updates *pb.Project) (*Notebook, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCMutateProject,
		Args:       []interface{}{projectID, updates},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("mutate project: %w", err)
	}

	var project pb.Project
	if err := beprotojson.Unmarshal(resp, &project); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &project, nil
}

func (c *Client) RemoveRecentlyViewedProject(projectID string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCRemoveRecentlyViewed,
		Args: []interface{}{projectID},
	})
	return err
}

// Source operations

/*
func (c *Client) AddSources(projectID string, sources []*pb.Source) ([]*pb.Source, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCAddSources,
		Args:       []interface{}{projectID, sources},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("add sources: %w", err)
	}

	var result []*pb.Source
	if err := beprotojson.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return result, nil
}
*/

func (c *Client) DeleteSources(projectID string, sourceIDs []string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCDeleteSources,
		Args: []interface{}{
			[][][]string{{sourceIDs}},
		},
		NotebookID: projectID,
	})
	return err
}

func (c *Client) MutateSource(sourceID string, updates *pb.Source) (*pb.Source, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCMutateSource,
		Args: []interface{}{sourceID, updates},
	})
	if err != nil {
		return nil, fmt.Errorf("mutate source: %w", err)
	}

	var source pb.Source
	if err := beprotojson.Unmarshal(resp, &source); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &source, nil
}

func (c *Client) RefreshSource(sourceID string) (*pb.Source, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCRefreshSource,
		Args: []interface{}{sourceID},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh source: %w", err)
	}

	var source pb.Source
	if err := beprotojson.Unmarshal(resp, &source); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &source, nil
}

func (c *Client) LoadSource(sourceID string) (*pb.Source, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCLoadSource,
		Args: []interface{}{sourceID},
	})
	if err != nil {
		return nil, fmt.Errorf("load source: %w", err)
	}

	var source pb.Source
	if err := beprotojson.Unmarshal(resp, &source); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &source, nil
}

/*
func (c *Client) CheckSourceFreshness(sourceID string) (*pb.CheckSourceFreshnessResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:   rpc.RPCCheckSourceFreshness,
		Args: []interface{}{sourceID},
	})
	if err != nil {
		return nil, fmt.Errorf("check source freshness: %w", err)
	}

	var result pb.CheckSourceFreshnessResponse
	if err := beprotojson.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &result, nil
}
*/

func (c *Client) ActOnSources(projectID string, action string, sourceIDs []string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCActOnSources,
		Args:       []interface{}{projectID, action, sourceIDs},
		NotebookID: projectID,
	})
	return err
}

// Source upload utility methods

func (c *Client) AddSourceFromReader(projectID string, r io.Reader, filename string) (string, error) {
	content, err := io.ReadAll(r)
	if err != nil {
		return "", fmt.Errorf("read content: %w", err)
	}

	contentType := http.DetectContentType(content)

	if strings.HasPrefix(contentType, "text/") {
		return c.AddSourceFromText(projectID, string(content), filename)
	}

	encoded := base64.StdEncoding.EncodeToString(content)
	return c.AddSourceFromBase64(projectID, encoded, filename, contentType)
}

func (c *Client) AddSourceFromText(projectID string, content, title string) (string, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCAddSources,
		NotebookID: projectID,
		Args: []interface{}{
			[]interface{}{
				[]interface{}{
					nil,
					[]string{
						title,
						content,
					},
					nil,
					2, // text source type
				},
			},
			projectID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("add text source: %w", err)
	}

	sourceID, err := extractSourceID(resp)
	if err != nil {
		return "", fmt.Errorf("extract source ID: %w", err)
	}
	return sourceID, nil
}

func (c *Client) AddSourceFromBase64(projectID string, content, filename, contentType string) (string, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCAddSources,
		NotebookID: projectID,
		Args: []interface{}{
			[]interface{}{
				[]interface{}{
					content,
					filename,
					contentType,
					"base64",
				},
			},
			projectID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("add binary source: %w", err)
	}

	sourceID, err := extractSourceID(resp)
	if err != nil {
		fmt.Fprintln(os.Stderr, resp)
		spew.Dump(resp)
		return "", fmt.Errorf("extract source ID: %w", err)
	}
	return sourceID, nil
}

func (c *Client) AddSourceFromFile(projectID string, filepath string) (string, error) {
	f, err := os.Open(filepath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	return c.AddSourceFromReader(projectID, f, filepath)
}

func (c *Client) AddSourceFromURL(projectID string, url string) (string, error) {
	// Check if it's a YouTube URL first
	if isYouTubeURL(url) {
		videoID, err := extractYouTubeVideoID(url)
		if err != nil {
			return "", fmt.Errorf("invalid YouTube URL: %w", err)
		}
		// Use dedicated YouTube method
		return c.AddYouTubeSource(projectID, videoID)
	}

	// Regular URL handling
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCAddSources,
		NotebookID: projectID,
		Args: []interface{}{
			[]interface{}{
				[]interface{}{
					nil,
					nil,
					[]string{url},
				},
			},
			projectID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("add source from URL: %w", err)
	}

	sourceID, err := extractSourceID(resp)
	if err != nil {
		return "", fmt.Errorf("extract source ID: %w", err)
	}
	return sourceID, nil
}

func (c *Client) AddYouTubeSource(projectID, videoID string) (string, error) {
	if c.rpc.Config.Debug {
		fmt.Printf("=== AddYouTubeSource ===\n")
		fmt.Printf("Project ID: %s\n", projectID)
		fmt.Printf("Video ID: %s\n", videoID)
	}

	// Modified payload structure for YouTube
	payload := []interface{}{
		[]interface{}{
			[]interface{}{
				nil,                                     // content
				nil,                                     // title
				videoID,                                 // video ID (not in array)
				nil,                                     // unused
				pb.SourceType_SOURCE_TYPE_YOUTUBE_VIDEO, // source type
			},
		},
		projectID,
	}

	if c.rpc.Config.Debug {
		fmt.Printf("\nPayload Structure:\n")
		spew.Dump(payload)
	}

	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCAddSources,
		NotebookID: projectID,
		Args:       payload,
	})
	if err != nil {
		return "", fmt.Errorf("add YouTube source: %w", err)
	}

	if c.rpc.Config.Debug {
		fmt.Printf("\nRaw Response:\n%s\n", string(resp))
	}

	if len(resp) == 0 {
		return "", fmt.Errorf("empty response from server (check debug output for request details)")
	}

	sourceID, err := extractSourceID(resp)
	if err != nil {
		return "", fmt.Errorf("extract source ID: %w", err)
	}
	return sourceID, nil
}

// Helper function to extract source ID with better error handling
func extractSourceID(resp json.RawMessage) (string, error) {
	if len(resp) == 0 {
		return "", fmt.Errorf("empty response")
	}

	var data []interface{}
	if err := json.Unmarshal(resp, &data); err != nil {
		return "", fmt.Errorf("parse response JSON: %w", err)
	}

	// Try different response formats
	// Format 1: [[[["id",...]]]]
	// Format 2: [[["id",...]]]
	// Format 3: [["id",...]]
	for _, format := range []func([]interface{}) (string, bool){
		// Format 1
		func(d []interface{}) (string, bool) {
			if len(d) > 0 {
				if d0, ok := d[0].([]interface{}); ok && len(d0) > 0 {
					if d1, ok := d0[0].([]interface{}); ok && len(d1) > 0 {
						if d2, ok := d1[0].([]interface{}); ok && len(d2) > 0 {
							if id, ok := d2[0].(string); ok {
								return id, true
							}
						}
					}
				}
			}
			return "", false
		},
		// Format 2
		func(d []interface{}) (string, bool) {
			if len(d) > 0 {
				if d0, ok := d[0].([]interface{}); ok && len(d0) > 0 {
					if d1, ok := d0[0].([]interface{}); ok && len(d1) > 0 {
						if id, ok := d1[0].(string); ok {
							return id, true
						}
					}
				}
			}
			return "", false
		},
		// Format 3
		func(d []interface{}) (string, bool) {
			if len(d) > 0 {
				if d0, ok := d[0].([]interface{}); ok && len(d0) > 0 {
					if id, ok := d0[0].(string); ok {
						return id, true
					}
				}
			}
			return "", false
		},
	} {
		if id, ok := format(data); ok {
			return id, nil
		}
	}

	return "", fmt.Errorf("could not find source ID in response structure: %v", data)
}

// Note operations

func (c *Client) CreateNote(projectID string, title string, initialContent string) (*Note, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCCreateNote,
		Args: []interface{}{
			projectID,
			initialContent,
			[]int{1}, // note type
			nil,
			title,
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("create note: %w", err)
	}

	var note Note
	if err := beprotojson.Unmarshal(resp, &note); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &note, nil
}

func (c *Client) MutateNote(projectID string, noteID string, content string, title string) (*Note, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCMutateNote,
		Args: []interface{}{
			projectID,
			noteID,
			[][][]interface{}{{
				{content, title, []interface{}{}},
			}},
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("mutate note: %w", err)
	}

	var note Note
	if err := beprotojson.Unmarshal(resp, &note); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &note, nil
}

func (c *Client) DeleteNotes(projectID string, noteIDs []string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCDeleteNotes,
		Args: []interface{}{
			[][][]string{{noteIDs}},
		},
		NotebookID: projectID,
	})
	return err
}

func (c *Client) GetNotes(projectID string) ([]*Note, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGetNotes,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("get notes: %w", err)
	}

	var response pb.GetNotesResponse
	if err := beprotojson.Unmarshal(resp, &response); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return response.Notes, nil
}

// Audio operations

func (c *Client) CreateAudioOverview(projectID string, instructions string) (*AudioOverviewResult, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID required")
	}
	if instructions == "" {
		return nil, fmt.Errorf("instructions required")
	}

	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCCreateAudioOverview,
		Args: []interface{}{
			projectID,
			0,
			[]string{
				instructions,
			},
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("create audio overview: %w", err)
	}

	var data []interface{}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse response JSON: %w", err)
	}

	result := &AudioOverviewResult{
		ProjectID: projectID,
	}

	// Handle empty or nil response
	if len(data) == 0 {
		return result, nil
	}

	// Parse the wrb.fr response format
	// Format: [null,null,[3,"<base64-audio>","<id>","<title>",null,true],null,[false]]
	if len(data) > 2 {
		audioData, ok := data[2].([]interface{})
		if !ok || len(audioData) < 4 {
			// Creation might be in progress, return result without error
			return result, nil
		}

		// Extract audio data (index 1)
		if audioBase64, ok := audioData[1].(string); ok {
			result.AudioData = audioBase64
		}

		// Extract ID (index 2)
		if id, ok := audioData[2].(string); ok {
			result.AudioID = id
		}

		// Extract title (index 3)
		if title, ok := audioData[3].(string); ok {
			result.Title = title
		}

		// Extract ready status (index 5)
		if len(audioData) > 5 {
			if ready, ok := audioData[5].(bool); ok {
				result.IsReady = ready
			}
		}
	}

	return result, nil
}

func (c *Client) GetAudioOverview(projectID string) (*AudioOverviewResult, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCGetAudioOverview,
		Args: []interface{}{
			projectID,
			1,
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("get audio overview: %w", err)
	}

	var data []interface{}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse response JSON: %w", err)
	}

	result := &AudioOverviewResult{
		ProjectID: projectID,
	}

	// Handle empty or nil response
	if len(data) == 0 {
		return result, nil
	}

	// Parse the wrb.fr response format
	// Format: [null,null,[3,"<base64-audio>","<id>","<title>",null,true],null,[false]]
	if len(data) > 2 {
		audioData, ok := data[2].([]interface{})
		if !ok || len(audioData) < 4 {
			return nil, fmt.Errorf("invalid audio data format")
		}

		// Extract audio data (index 1)
		if audioBase64, ok := audioData[1].(string); ok {
			result.AudioData = audioBase64
		}

		// Extract ID (index 2)
		if id, ok := audioData[2].(string); ok {
			result.AudioID = id
		}

		// Extract title (index 3)
		if title, ok := audioData[3].(string); ok {
			result.Title = title
		}

		// Extract ready status (index 5)
		if len(audioData) > 5 {
			if ready, ok := audioData[5].(bool); ok {
				result.IsReady = ready
			}
		}
	}

	return result, nil
}

// AudioOverviewResult represents an audio overview response
type AudioOverviewResult struct {
	ProjectID string
	AudioID   string
	Title     string
	AudioData string // Base64 encoded audio data
	IsReady   bool
}

// GetAudioBytes returns the decoded audio data
func (r *AudioOverviewResult) GetAudioBytes() ([]byte, error) {
	if r.AudioData == "" {
		return nil, fmt.Errorf("no audio data available")
	}
	return base64.StdEncoding.DecodeString(r.AudioData)
}

func (c *Client) DeleteAudioOverview(projectID string) error {
	_, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCDeleteAudioOverview,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	return err
}

// Generation operations

func (c *Client) GenerateDocumentGuides(projectID string) (*pb.GenerateDocumentGuidesResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGenerateDocumentGuides,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("generate document guides: %w", err)
	}

	var guides pb.GenerateDocumentGuidesResponse
	if err := beprotojson.Unmarshal(resp, &guides); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &guides, nil
}

func (c *Client) GenerateNotebookGuide(projectID string) (*pb.GenerateNotebookGuideResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGenerateNotebookGuide,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("generate notebook guide: %w", err)
	}

	var guide pb.GenerateNotebookGuideResponse
	if err := beprotojson.Unmarshal(resp, &guide); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &guide, nil
}

func (c *Client) GenerateOutline(projectID string) (*pb.GenerateOutlineResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGenerateOutline,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("generate outline: %w", err)
	}

	var outline pb.GenerateOutlineResponse
	if err := beprotojson.Unmarshal(resp, &outline); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &outline, nil
}

func (c *Client) GenerateSection(projectID string) (*pb.GenerateSectionResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCGenerateSection,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("generate section: %w", err)
	}

	var section pb.GenerateSectionResponse
	if err := beprotojson.Unmarshal(resp, &section); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &section, nil
}

func (c *Client) StartDraft(projectID string) (*pb.StartDraftResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCStartDraft,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("start draft: %w", err)
	}

	var draft pb.StartDraftResponse
	if err := beprotojson.Unmarshal(resp, &draft); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &draft, nil
}

func (c *Client) StartSection(projectID string) (*pb.StartSectionResponse, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID:         rpc.RPCStartSection,
		Args:       []interface{}{projectID},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("start section: %w", err)
	}

	var section pb.StartSectionResponse
	if err := beprotojson.Unmarshal(resp, &section); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &section, nil
}

// Sharing operations

// ShareOption represents audio sharing visibility options
type ShareOption int

const (
	SharePrivate ShareOption = 0
	SharePublic  ShareOption = 1
)

// ShareAudioResult represents the response from sharing audio
type ShareAudioResult struct {
	ShareURL string
	ShareID  string
	IsPublic bool
}

// ShareAudio shares an audio overview with optional public access
func (c *Client) ShareAudio(projectID string, shareOption ShareOption) (*ShareAudioResult, error) {
	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCShareAudio,
		Args: []interface{}{
			[]int{int(shareOption)},
			projectID,
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("share audio: %w", err)
	}

	// Parse the raw response
	var data []interface{}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ShareAudioResult{
		IsPublic: shareOption == SharePublic,
	}

	// Extract share URL and ID from response
	if len(data) > 0 {
		if shareData, ok := data[0].([]interface{}); ok && len(shareData) > 0 {
			if shareURL, ok := shareData[0].(string); ok {
				result.ShareURL = shareURL
			}
			if len(shareData) > 1 {
				if shareID, ok := shareData[1].(string); ok {
					result.ShareID = shareID
				}
			}
		}
	}

	return result, nil
}

// Question/Answer operations

// AnswerResult represents the response from asking a question
type AnswerResult struct {
	ProjectID string
	Question  string
	Answer    string
	Sources   []string
}

// AskQuestion asks a question about the notebook content and returns an AI-generated answer
// It will wait for the answer to be generated by polling the API
func (c *Client) AskQuestion(projectID string, question string) (*AnswerResult, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project ID required")
	}
	if question == "" {
		return nil, fmt.Errorf("question required")
	}

	// Start the question processing
	resp, err := c.rpc.Do(rpc.Call{
		ID: rpc.RPCGuidebookGenerateAnswer,
		Args: []interface{}{
			projectID,
			question,
			nil, // Additional parameters (if needed)
		},
		NotebookID: projectID,
	})
	if err != nil {
		return nil, fmt.Errorf("ask question: %w", err)
	}

	var data []interface{}
	if err := json.Unmarshal(resp, &data); err != nil {
		return nil, fmt.Errorf("parse response JSON: %w", err)
	}

	result := &AnswerResult{
		ProjectID: projectID,
		Question:  question,
	}

	// Check if we got an immediate response or need to poll
	if len(data) > 0 {
		// Check if response indicates processing (like [3])
		if len(data) == 1 {
			if statusCode, ok := data[0].(float64); ok && statusCode == 3 {
				// Status code 3 likely means "processing" - need to poll for result
				return c.pollForAnswer(projectID, question, result)
			}
		}

		// Try to parse immediate answer
		if answer := c.parseAnswerFromResponse(data); answer != "" {
			result.Answer = answer
			return result, nil
		}
	}

	// If we couldn't parse the answer, include the raw response for debugging
	rawData, _ := json.Marshal(data)
	result.Answer = fmt.Sprintf("Raw response: %s", string(rawData))
	return result, nil
}

// pollForAnswer polls the API until an answer is ready
func (c *Client) pollForAnswer(projectID, question string, result *AnswerResult) (*AnswerResult, error) {
	const maxRetries = 60  // Increased to 2 minutes
	const pollInterval = 2 // Seconds between polls

	fmt.Fprintf(os.Stderr, "Waiting for response")

	for i := 0; i < maxRetries; i++ {
		// Wait before next poll (except first attempt)
		if i > 0 {
			time.Sleep(time.Duration(pollInterval) * time.Second)
			fmt.Fprintf(os.Stderr, ".")
		}

		// Try different approaches for polling
		// First, try the original endpoint
		resp, err := c.rpc.Do(rpc.Call{
			ID: rpc.RPCGuidebookGenerateAnswer,
			Args: []interface{}{
				projectID,
				question,
				nil,
			},
			NotebookID: projectID,
		})
		if err != nil {
			// If that fails, try GetGuidebook to see if there's a different way
			if i%5 == 0 { // Try every 5 attempts
				resp, err = c.rpc.Do(rpc.Call{
					ID: rpc.RPCGetGuidebook,
					Args: []interface{}{
						projectID,
					},
					NotebookID: projectID,
				})
			}
			if err != nil {
				continue // Continue polling on error
			}
		}

		var data []interface{}
		if err := json.Unmarshal(resp, &data); err != nil {
			continue // Continue polling on parse error
		}

		// Check if we now have a real answer
		if len(data) > 0 {
			// Still processing if we get [3]
			if len(data) == 1 {
				if statusCode, ok := data[0].(float64); ok && statusCode == 3 {
					continue // Still processing, continue polling
				}
			}

			// Try to parse the answer
			if answer := c.parseAnswerFromResponse(data); answer != "" {
				fmt.Fprintf(os.Stderr, " ✓\n")
				result.Answer = answer
				return result, nil
			}
		}
	}

	fmt.Fprintf(os.Stderr, " ✗\n")
	// Timeout reached
	return nil, fmt.Errorf("timeout waiting for answer after %d attempts (%d seconds)", maxRetries, maxRetries*pollInterval)
}

// parseAnswerFromResponse extracts the answer from various response formats
func (c *Client) parseAnswerFromResponse(data []interface{}) string {
	if len(data) == 0 {
		return ""
	}

	// Try different parsing strategies

	// Strategy 1: Direct string response
	if answerStr, ok := data[0].(string); ok {
		return answerStr
	}

	// Strategy 2: Map with answer field
	if answerData, ok := data[0].(map[string]interface{}); ok {
		if answer, ok := answerData["answer"].(string); ok {
			return answer
		}
		if answer, ok := answerData["content"].(string); ok {
			return answer
		}
		if answer, ok := answerData["text"].(string); ok {
			return answer
		}
	}

	// Strategy 3: Nested array structure (like audio overview)
	if len(data) > 2 {
		if answerArray, ok := data[2].([]interface{}); ok && len(answerArray) > 0 {
			if answer, ok := answerArray[0].(string); ok {
				return answer
			}
			// Check if it's at index 1 like audio data
			if len(answerArray) > 1 {
				if answer, ok := answerArray[1].(string); ok {
					return answer
				}
			}
		}
	}

	// Strategy 4: Look for text in any nested structure
	for _, item := range data {
		if itemArray, ok := item.([]interface{}); ok {
			for _, subItem := range itemArray {
				if text, ok := subItem.(string); ok && len(text) > 10 { // Assume real answers are longer than 10 chars
					return text
				}
			}
		}
	}

	return ""
}

// Helper functions to identify and extract YouTube video IDs
func isYouTubeURL(url string) bool {
	return strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be")
}

func extractYouTubeVideoID(urlStr string) (string, error) {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}

	if u.Host == "youtu.be" {
		return strings.TrimPrefix(u.Path, "/"), nil
	}

	if strings.Contains(u.Host, "youtube.com") && u.Path == "/watch" {
		return u.Query().Get("v"), nil
	}

	return "", fmt.Errorf("unsupported YouTube URL format")
}
