package panel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBootstrapHAProxyLogsInitialSetting(t *testing.T) {
	const base = `"name":"edge-1","address":"192.0.2.10","username":"root","password":"ssh-secret","host_key_sha256":"SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"`
	cases := []struct {
		name    string
		extra   string
		present bool
	}{
		{name: "omitted keeps default", extra: ""},
		{name: "true keeps default", extra: `,"haproxy_logs":true`},
		{name: "false stored on node", extra: `,"haproxy_logs":false`, present: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{}
			installer := &fakeInstaller{}
			h := NewHandlerWithBootstrap(store, Config{AdminToken: testAdminToken}, installer)
			created, _ := startBootstrapJob(t, h, `{`+base+tc.extra+`}`)
			installed, pollBody := waitBootstrapJob(t, h, created.JobID)
			require.Equal(t, bootstrapJobInstalled, installed.Status, pollBody)
			value, ok := store.createdNode.Metadata[nodeMetadataHAProxyLogsKey]
			if tc.present {
				require.True(t, ok)
				assert.Equal(t, false, value)
				assert.False(t, nodeMetadataHAProxyLogs(store.createdNode.Metadata))
			} else {
				assert.False(t, ok, "metadata must stay byte-identical to pre-toggle nodes")
				assert.True(t, nodeMetadataHAProxyLogs(store.createdNode.Metadata))
			}
		})
	}
}
