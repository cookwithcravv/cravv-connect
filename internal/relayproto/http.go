package relayproto

// JSON bodies of the relay-v1 HTTP endpoints.

// BlobCreateRequest is the JSON body of POST /v1/blobs.
type BlobCreateRequest struct {
	Size      int64  `json:"size"`
	Chunks    uint32 `json:"chunks"`
	Recipient string `json:"recipient"` // recipient IK, B64
}

// BlobCreateResponse is the JSON body of the 201 reply to POST /v1/blobs.
type BlobCreateResponse struct {
	BlobID string `json:"blob_id"`
}

// HTTPErrorBody is the JSON body of every non-2xx HTTP response.
type HTTPErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Health is the JSON body of GET /v1/health.
type Health struct {
	OK      bool `json:"ok"`
	Version int  `json:"version"`
}
