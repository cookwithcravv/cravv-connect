package conformance

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/cookwithcravv/cravv-connect/internal/relayproto"
)

func healthCases() []testCase {
	return []testCase{
		{"health/ok_and_version", func(t *testing.T, s *suite) {
			req, err := http.NewRequestWithContext(ctxT(t), http.MethodGet, s.client.Origin()+relayproto.PathHealth, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var h relayproto.Health
			if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&h) != nil || !h.OK || h.Version != relayproto.Version {
				t.Fatalf("status %d health %+v", resp.StatusCode, h)
			}
		}},
	}
}
