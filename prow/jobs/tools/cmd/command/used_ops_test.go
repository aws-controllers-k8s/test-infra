package command

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScanUsedOps(t *testing.T) {
	got, err := scanUsedOps(fakeControllerPath)
	require.NoError(t, err)

	// Snake_case `gizmo_widget` must be found under its normalized key; single-word `widget`
	// would not catch a plain strings.ToLower regression.
	require.Contains(t, got, "gizmowidget",
		"a snake_case resource directory must be keyed by its normalized name")
	assert.True(t, got["gizmowidget"]["CreateGizmoWidget"])

	// A mock referenced only from a _test.go must NOT register as used.
	assert.False(t, got["gizmowidget"]["DeleteGizmoWidgetMockOnly"],
		"operations named only in _test.go must be excluded")

	require.Contains(t, got, "widget")
	ops := got["widget"]

	// Found in sdk.go.
	assert.True(t, ops["CreateWidget"])
	assert.True(t, ops["DeleteWidget"])
	// Found only in hook.go — the case model.CRD.Ops would miss.
	assert.True(t, ops["PutWidgetTagging"])
	// Found via the call-site form with an inline input literal.
	assert.True(t, ops["GetWidgetConfig"])

	assert.False(t, ops["CreateGadget"])
}

func TestScanUsedOpsFailsOnUnreadableFile(t *testing.T) {
	// An unreadable file is an error, not a silent gap in the operation set.
	if os.Getuid() == 0 {
		t.Skip("running as root: chmod 0000 does not make a file unreadable")
	}

	root := t.TempDir()
	dir := filepath.Join(root, "pkg", "resource", "widget")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	path := filepath.Join(dir, "sdk.go")
	require.NoError(t, os.WriteFile(path, []byte("package widget\n"), 0o644))
	require.NoError(t, os.Chmod(path, 0o000))
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	got, err := scanUsedOps(root)
	require.Error(t, err, "an unreadable file must fail the controller")
	assert.Contains(t, err.Error(), "sdk.go", "the error must name the file")
	assert.Nil(t, got, "no partial operation set may escape")
}

func TestScanUsedOpsNoDir(t *testing.T) {
	got, err := scanUsedOps("../../../testdata/does-not-exist")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestNormalizeResourceKey(t *testing.T) {
	// Real kind/directory pairs from ec2-controller, which naive lowercasing breaks.
	pairs := []struct {
		kind string
		dir  string
	}{
		{"DHCPOptions", "dhcp_options"},
		{"VPCEndpoint", "vpc_endpoint"},
		{"NATGateway", "nat_gateway"},
		{"NetworkACL", "network_acl"},
		{"ElasticIPAddress", "elastic_ip_address"},
		{"TransitGatewayVPCAttachment", "transit_gateway_vpc_attachment"},
		{"VPCEndpointServiceConfiguration", "vpc_endpoint_service_configuration"},
		// Single-word kinds must keep working too.
		{"Instance", "instance"},
		{"VPC", "vpc"},
		{"Bucket", "bucket"},
	}

	for _, p := range pairs {
		t.Run(p.kind, func(t *testing.T) {
			assert.Equal(t, normalizeResourceKey(p.dir), normalizeResourceKey(p.kind),
				"kind %q and directory %q must normalize to the same key", p.kind, p.dir)
		})
	}

	// Guard against "simplifying" back to naive lowercasing.
	assert.NotEqual(t, "dhcp_options", strings.ToLower("DHCPOptions"),
		"lowercasing alone does not reach the directory name")
}

func TestAllUsedOps(t *testing.T) {
	byResource := map[string]map[string]bool{
		"widget": {"CreateWidget": true},
		"gadget": {"CreateGadget": true, "CreateWidget": true},
	}
	got := allUsedOps(byResource)
	assert.True(t, got["CreateWidget"])
	assert.True(t, got["CreateGadget"])
	assert.Len(t, got, 2)
}
