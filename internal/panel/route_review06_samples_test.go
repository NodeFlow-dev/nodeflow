package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// reviewSamples are the 11 scenarios of review 06 as API payloads. Shapes
// that the review showed to be broken are now rejected by validation; for
// those the sample renders the closest valid layout and wantErr records the
// rejection of the original payload.
var reviewSamples = []struct {
	file, payload, rejected, wantErr string
}{
	{file: "01-pool-static-sticky", payload: `{"name":"s01","match_mode":"fallback","listener_port":10001,"balance_mode":"pool","sticky_enabled":true,
		"servers":[{"name":"a","host":"192.0.2.1","port":443},{"name":"b","host":"192.0.2.2","port":443}]}`},
	{file: "02-failover-static", payload: `{"name":"s02","match_mode":"fallback","listener_port":10002,"balance_mode":"failover","proxy_protocol":"v2",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"s","host":"192.0.2.2","port":443},{"name":"t","host":"2001:db8::1","port":443}]}`},
	{file: "03-pool-mixed-ip-first", payload: `{"name":"s03","match_mode":"fallback","listener_port":10003,"balance_mode":"pool","dns_pool":true,
		"servers":[{"name":"st","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`},
	{file: "04-failover-dns-backup", payload: `{"name":"s04","match_mode":"fallback","listener_port":10004,"balance_mode":"failover","dns_pool":true,
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`},
	{file: "05-failover-dns-first-pref", payload: `{"name":"s05","match_mode":"fallback","listener_port":10005,"balance_mode":"failover",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"}]}`,
		rejected: `{"name":"s05","match_mode":"fallback","listener_port":10005,"balance_mode":"failover",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"},{"name":"s","host":"192.0.2.2","port":443}]}`,
		wantErr: "DNS-pool reserve must be the only reserve"},
	{file: "06-pool-dns-pref-ipv6", payload: `{"name":"s06","match_mode":"fallback","listener_port":10006,
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"2001:db8::10"}]}`,
		rejected: `{"name":"s06","match_mode":"fallback","listener_port":10006,"balance_mode":"pool",
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"2001:db8::10"}]}`,
		wantErr: "preferred_ip is only valid for the first server in failover mode"},
	{file: "07-name-collision", payload: `{"name":"s07","match_mode":"fallback","listener_port":10007,"balance_mode":"pool",
		"servers":[{"name":"x","host":"example.com","port":443,"dns_pool":true},{"name":"y","host":"192.0.2.1","port":443}]}`,
		rejected: `{"name":"s07","match_mode":"fallback","listener_port":10007,"balance_mode":"pool",
		"servers":[{"name":"x","host":"example.com","port":443,"dns_pool":true},{"name":"x_1","host":"192.0.2.1","port":443}]}`,
		wantErr: "collides with DNS-pool server"},
	{file: "08-ui-payload-dns-first", payload: `{"name":"s08","match_mode":"fallback","listener_port":10008,"balance_mode":"pool","dns_pool":true,
		"target_host":"example.com","target_port":443,
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true},{"name":"st","host":"192.0.2.2","port":443}]}`},
	{file: "09-nohealth-proxy-dns", payload: `{"name":"s09","match_mode":"fallback","listener_port":10009,"balance_mode":"failover","health_check":false,"proxy_protocol":"v1",
		"servers":[{"name":"p","host":"192.0.2.1","port":443},{"name":"dp","host":"example.com","port":443,"dns_pool":true}]}`},
	{file: "10-failover-dns-pref-sticky-ignored", payload: `{"name":"s10","match_mode":"fallback","listener_port":10010,"balance_mode":"failover","sticky_enabled":true,
		"servers":[{"name":"dp","host":"example.com","port":443,"dns_pool":true,"preferred_ip":"192.0.2.10"}]}`},
	{file: "11-ui-single-server-legacy", payload: `{"name":"s11","match_mode":"fallback","listener_port":10011,"balance_mode":"pool",
		"quota_bytes":1073741824,"quota_action":"block_new","servers":[{"name":"target","host":"192.0.2.1","port":443}]}`},
}

// TestReviewSamplesRender renders every review-06 sample through
// validateRoute and the renderer. With NODEFLOW_RENDER_SAMPLES_DIR set, the
// configs are written there for an external `haproxy -c` check.
func TestReviewSamplesRender(t *testing.T) {
	dir := os.Getenv("NODEFLOW_RENDER_SAMPLES_DIR")
	for _, sample := range reviewSamples {
		t.Run(sample.file, func(t *testing.T) {
			if sample.rejected != "" {
				_, err := validatePayload(t, sample.rejected)
				require.Error(t, err)
				require.Contains(t, err.Error(), sample.wantErr)
			}
			_, got := renderPayload(t, sample.payload)
			require.Contains(t, got.Config, "\nbackend nf_be_")
			if strings.Contains(got.Config, "resolvers nf_dns") {
				require.Contains(t, got.Config, "\nresolvers nf_dns\n")
			}
			if dir != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, sample.file+".cfg"), []byte(got.Config), 0o644))
			}
		})
	}
}
