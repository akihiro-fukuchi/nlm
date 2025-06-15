# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

### Building and Running
```bash
# Build the CLI tool
go build ./cmd/nlm

# Install to GOPATH/bin
go install ./cmd/nlm

# Run directly
go run ./cmd/nlm <command>
```

### Testing
```bash
# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run specific package tests
go test ./internal/batchexecute/
```

### Protocol Buffer Code Generation
```bash
# Generate protobuf code (requires buf CLI tool)
cd proto && buf generate

# The generated code goes into gen/ directory
# Generated files are committed to the repository
```

## Architecture Overview

### Core Components

**CLI Command Dispatcher (`cmd/nlm/main.go`)**
- Single main function with command-based routing
- Auto-retry authentication with 3 attempts
- Global flags: `-auth`, `-cookies`, `-debug`

**API Client Architecture (`internal/api/client.go`)**
- Uses custom batchexecute RPC system to communicate with Google's NotebookLM backend
- Each operation is an RPC call with specific endpoint IDs
- Response parsing handles complex nested JSON structures from Google's API
- Custom protocol for different source types (text, files, URLs, YouTube)

**Authentication System (`internal/auth/`)**
- Browser-based authentication using Chrome DevTools Protocol
- Extracts auth tokens and cookies from live browser session
- Platform-specific Chrome detection (Darwin, Linux, Windows)
- Creates temporary browser profiles for auth extraction

**BatchExecute RPC System (`internal/batchexecute/`)**
- Custom implementation of Google's internal RPC protocol
- Handles chunked responses and batch operations
- Decodes complex response formats with nested arrays
- Request ID generation and URL parameter management

**Protocol Buffer Integration**
- Uses buf for code generation and management
- Generated code in `gen/notebooklm/v1alpha1/`
- Custom JSON marshaling for backend compatibility (`internal/beprotojson/`)

### Key Patterns

**RPC Call Structure**
```go
resp, err := c.rpc.Do(rpc.Call{
    ID:         "endpoint_id",
    Args:       []interface{}{arg1, arg2},
    NotebookID: projectID, // for notebook-specific calls
})
```

**Response Parsing**
- Complex nested array structures from Google's backend
- Fallback parsing for different response formats
- Extract IDs and metadata from deeply nested JSON

**Error Handling**
- `ErrUnauthorized` triggers automatic re-authentication
- Retry logic with exponential backoff
- Debug mode for request/response inspection

## Protocol Buffer Workflow

The project uses protocol buffers for API definitions:

1. **Proto files**: Located in `proto/notebooklm/v1alpha1/`
2. **Configuration**: `proto/buf.yaml` and `proto/buf.gen.yaml`
3. **Generated code**: Output to `gen/notebooklm/v1alpha1/`
4. **Dependencies**: Uses `buf.build/googleapis/googleapis`

When modifying proto files:
1. Edit `.proto` files in `proto/notebooklm/v1alpha1/`
2. Run `cd proto && buf generate`
3. Commit both proto and generated files

## Testing Strategy

- Unit tests for core components (`batchexecute`, `beprotojson`)
- Embedded test data in `testdata/` directories
- Integration tests using httptest for HTTP interactions
- Response parsing validation with real API response samples

## Development Notes

**Authentication Flow**
- Requires Google account with NotebookLM access
- Uses browser automation to extract session tokens
- Stores credentials in `.env` file or environment variables

**Backend Integration**
- Reverse-engineered Google NotebookLM web client API
- Custom protocol implementation for batch operations
- Complex response parsing due to proprietary format

**Source Type Handling**
- Text sources: Direct content upload
- File sources: Base64 encoding with content type detection
- URL sources: Backend fetches content automatically
- YouTube sources: Special video ID extraction and formatting

**Error Debugging**
- Use `-debug` flag for detailed request/response logging
- Response structure inspection with `spew.Dump`
- Network-level debugging through Chrome DevTools