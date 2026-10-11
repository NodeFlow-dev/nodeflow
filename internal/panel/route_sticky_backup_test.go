package panel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStickyDNSPoolWithReserve(t *testing.T) {
	for _, layout := range []struct{ name, servers string }{
		{"dns-primary", `{"name":"primary","host":"pool.example.com","port":443,"dns_pool":true},{"name":"reserve","host":"192.0.2.20","port":443,"backup":true}`},
		{"dns-reserve", `{"name":"primary","host":"192.0.2.10","port":443},{"name":"reserve","host":"pool.example.com","port":443,"dns_pool":true,"backup":true}`},
		{"two-primaries", `{"name":"primary","host":"192.0.2.10","port":443},{"name":"second","host":"192.0.2.11","port":443},{"name":"reserve","host":"192.0.2.20","port":443,"backup":true}`},
	} {
		t.Run(layout.name, func(t *testing.T) {
			spec, got := renderPayload(t, `{"name":"sticky-backup","match_mode":"fallback","listener_port":19443,"sticky_mode":"source_table","sticky_ttl":"24h","balance_algorithm":"leastconn","servers":[`+layout.servers+`]}`)
			require.Equal(t, "failover", spec.BalanceMode)
			require.Equal(t, "source_table", spec.StickyMode)
			require.Equal(t, "24h", spec.StickyTTL)
			require.Equal(t, "leastconn", spec.BalanceAlgorithm)
			require.Contains(t, got.Config, "balance leastconn")
			require.Contains(t, got.Config, "expire 24h peers nf_peers")
			require.Contains(t, got.Config, "option redispatch")
			require.Contains(t, got.Config, "peers nf_peers")
			require.Contains(t, got.Config, " backup\n")
			require.True(t, spec.Servers[len(spec.Servers)-1].Backup)
			for _, server := range spec.Servers[:len(spec.Servers)-1] {
				require.False(t, server.Backup)
			}
			// Stored routes are validated again when a node revision is rendered.
			again, err := RenderHAProxyConfig([]Route{specToRoute(spec)})
			require.NoError(t, err)
			require.Equal(t, got.Config, again.Config)
			if dir := os.Getenv("NODEFLOW_RENDER_SAMPLES_DIR"); dir != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "sticky-"+layout.name+".cfg"), []byte(got.Config), 0600))
			}
			plain := specToRoute(spec)
			plain.StickyMode, plain.StickyTTL, plain.BalanceAlgorithm, plain.StickyEnabled = "", "", "", false
			legacy, err := RenderHAProxyConfig([]Route{plain})
			require.NoError(t, err)
			require.False(t, strings.Contains(legacy.Config, "stick-table"))
		})
	}
}

func TestBackupDistributionRejectsAgentManagedAlgorithms(t *testing.T) {
	for _, distribution := range []string{
		`"balance_algorithm":"leastping"`,
		`"balance_algorithm":"leastconn","balance_tolerance":0.1`,
	} {
		_, err := validatePayload(t, `{"name":"backup","match_mode":"fallback","balance_mode":"failover","sticky_mode":"source_table",`+distribution+`,`+poolServers+`}`)
		require.ErrorContains(t, err, "native HAProxy algorithms only")
	}
}
