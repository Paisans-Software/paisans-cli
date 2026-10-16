// What host admit reuses from host prepare, exported.
package hostprep

import (
	"strings"
	"text/template"
)

// EgressData is what the egress table is rendered with: the deployment's
// token and table, the mesh's mark as nft writes it, the user's UID and
// subordinate range as an nft set's elements ("1001, 165536-231071"), the
// mesh subnet, the host's resolvers by family, and with --mesh-lan the
// host's own LAN prefix.
type EgressData struct {
	Token, Table, Mark, Users, Mesh, LAN string
	Resolvers4, Resolvers6               []string
}

// EgressTable is the nftables file host admit loads for a deployment.
func EgressTable(d EgressData) (string, error) {
	return snippetFuncs("snippets/egress.nft.tmpl", template.FuncMap{"join": strings.Join}, d)
}

// EgressUnitData is what the egress unit is rendered with.
type EgressUnitData struct{ Token, User, Table, File, UID string }

// EgressUnit is the system unit that loads the egress table at boot.
func EgressUnit(d EgressUnitData) (string, error) {
	return snippet("snippets/paisans-egress.service.tmpl", d)
}
