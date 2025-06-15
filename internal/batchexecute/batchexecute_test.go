package batchexecute

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

//go:embed testdata/*txt
var testdata embed.FS

func TestDecodeResponse(t *testing.T) {
	testCases := []struct {
		name      string
		input     string
		inputFile string
		chunked   bool
		expected  []Response
		validate  func(t *testing.T, resp []Response) // Optional validation function
		err       error
	}{
		{
			name:      "List Notebooks Response",
			inputFile: "list_notebooks.txt",
			chunked:   false,
			validate: func(t *testing.T, resp []Response) {
				if len(resp) != 1 {
					t.Errorf("Expected 1 response, got %d", len(resp))
					return
				}
			},
			err: nil,
		},
		{
			name: "Error Response",
			input: `94
[["wrb.fr","error","[{\"error\":\"Invalid request\",\"code\":400}]",null,null,null,"generic"]]`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				if len(resp) != 1 {
					t.Errorf("Expected 1 response, got %d", len(resp))
					return
				}
				if resp[0].ID != "error" {
					t.Errorf("Expected ID 'error', got %s", resp[0].ID)
				}
				// Just verify the data contains the expected fields
				if !strings.Contains(string(resp[0].Data), "error") || !strings.Contains(string(resp[0].Data), "400") {
					t.Errorf("Response data doesn't contain expected error fields: %s", string(resp[0].Data))
				}
			},
			err: nil,
		},
		{
			name: "Multiple Chunk Types",
			input: `145
[["wrb.fr","VUsiyb","[null,null,[3,null,\"fec1780c-5a14-4f07-8ee6-f8c3ee2930fa\",\"nbname2\",null,true],null,[false]]",null,null,null,"generic"]]
23
[["e",4,null,null,237]]
55
[["di",125],["af.httprm",124,"6343297907846200142",27]]`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				if len(resp) != 1 {
					t.Errorf("Expected 1 response, got %d", len(resp))
					return
				}

				// Verify the main response
				if resp[0].ID != "VUsiyb" {
					t.Errorf("Expected ID VUsiyb, got %s", resp[0].ID)
				}
			},
			err: nil,
		},
		{
			name: "Deeply Nested JSON",
			input: `217
[["wrb.fr","nested","[{\"data\":{\"items\":[{\"id\":\"test\",\"metadata\":{\"created\":1234567890,\"modified\":1234567891},\"content\":{\"text\":\"Hello, World!\",\"format\":\"plain\"}}]}}]",null,null,null,"generic"]]`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				if len(resp) != 1 {
					t.Errorf("Expected 1 response, got %d", len(resp))
					return
				}

				// The response data is an array, so we need to parse it as such first
				var dataArray []interface{}
				if err := json.Unmarshal(resp[0].Data, &dataArray); err != nil {
					t.Errorf("Failed to parse data as array: %v", err)
					return
				}

				// Just verify we have valid JSON structure
				if len(dataArray) == 0 {
					t.Errorf("Expected non-empty data array")
				}
			},
			err: nil,
		},
		{
			name: "YouTube Source Addition Response",
			input: `)]}'
50
[["wrb.fr","izAoDd",null,null,null,[3],"generic"]]
23
[["e",4,null,null,237]]`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				if len(resp) != 1 {
					t.Errorf("Expected 1 response, got %d", len(resp))
					return
				}
				if resp[0].ID != "izAoDd" {
					t.Errorf("Expected ID izAoDd, got %s", resp[0].ID)
				}
			},
			err: nil,
		},
		{
			name: "Invalid Chunk Length",
			input: `abc
[["wrb.fr","test","data",null,null,null,"generic"]]`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				// Should not reach here
				t.Errorf("Should not have succeeded with invalid chunk length")
			},
		},
		{
			name: "Incomplete Chunk",
			input: `100
[["wrb.fr","test","`,
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				// Should not reach here
				t.Errorf("Should not have succeeded with incomplete chunk")
			},
		},
		{
			name:    "Empty Response",
			input:   "",
			chunked: true,
			validate: func(t *testing.T, resp []Response) {
				// Should not reach here
				t.Errorf("Should not have succeeded with empty response")
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {

			if tc.inputFile != "" {
				content, err := testdata.ReadFile("testdata/" + tc.inputFile)
				if err != nil {
					t.Errorf("Failed to read test data: %v", err)
					return
				}
				tc.input = string(content)
			}

			var (
				actual []Response
				err    error
			)

			if tc.chunked {
				actual, err = decodeChunkedResponse(tc.input)
			} else {
				actual, err = decodeResponse(tc.input)
			}

			// For error test cases, check that we get an error
			errorTestCases := []string{"Invalid Chunk Length", "Incomplete Chunk", "Empty Response"}
			isErrorTest := false
			for _, errorCase := range errorTestCases {
				if tc.name == errorCase {
					isErrorTest = true
					break
				}
			}

			if isErrorTest {
				if err == nil {
					t.Errorf("Expected an error for test case %s, but got none", tc.name)
				}
				return // Don't check other things for error cases
			}

			// Check error for non-error test cases
			if tc.err != nil && !cmp.Equal(err, tc.err, cmpopts.EquateErrors()) {
				t.Errorf("Error mismatch (-want +got):\n%s", cmp.Diff(tc.err, err, cmpopts.EquateErrors()))
			}

			// If there's a validation function, use it
			if err == nil && tc.validate != nil {
				tc.validate(t, actual)
			}

			// If there are expected responses, compare them
			if err == nil && tc.expected != nil && !cmp.Equal(actual, tc.expected) {
				t.Errorf("Response mismatch (-want +got):\n%s", cmp.Diff(tc.expected, actual))
			}
		})
	}
}

func TestExecute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Log("Received request")

		// Verify request format
		if err := r.ParseForm(); err != nil {
			t.Errorf("Failed to parse form: %v", err)
			return
		}

		if r.Form.Get("f.req") == "" {
			t.Error("Missing f.req parameter")
			return
		}

		w.WriteHeader(http.StatusOK)
		// Return realistic response format
		fmt.Fprintf(w, `)]}'

[["wrb.fr","VUsiyb","[null,null,[3,null,\"fec1780c-5a14-4f07-8ee6-f8c3ee2930fa\",\"nbname2\",null,true],null,[false]]",null,null,null,"generic"]]`)
	}))
	defer server.Close()

	config := Config{
		Host:      strings.TrimPrefix(server.URL, "http://"),
		App:       "notebooklm",
		AuthToken: "test_token",
		Headers:   map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		UseHTTP:   true,
	}
	client := NewClient(config, WithHTTPClient(server.Client()))

	rpc := RPC{
		ID:    "VUsiyb",
		Args:  []interface{}{nil, 1},
		Index: "generic",
	}

	response, err := client.Execute([]RPC{rpc})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
		return
	}

	expectedData := json.RawMessage(`[null,null,[3,null,"fec1780c-5a14-4f07-8ee6-f8c3ee2930fa","nbname2",null,true],null,[false]]`)
	if string(response.Data) != string(expectedData) {
		t.Errorf("Unexpected response data:\ngot:  %s\nwant: %s", string(response.Data), string(expectedData))
	}
}
