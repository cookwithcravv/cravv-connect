package relayproto

// HTTP paths and query parameters of relay-v1.
const (
	PathConnect = "/v1/connect"
	PathPair    = "/v1/pair/" // + nameplate
	PathBlobs   = "/v1/blobs"
	PathHealth  = "/v1/health"
	QueryIK     = "ik"    // routing hint on /v1/connect: B64 of the identity public key
	QueryToken  = "token" // creator token on /v1/pair/{nameplate}
)

// ChunkOverhead is the per-chunk allowance above FileChunkBytes for AEAD tags.
const ChunkOverhead = 64
