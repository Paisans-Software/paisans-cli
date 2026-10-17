package admitrules

// PrivilegedGroups are the groups a deployment's user must not be in: each
// reaches past the user, to root (sudo, wheel, admin, lxd), to every user's
// logs (adm, systemd-journal), or to the owner's system Docker daemon,
// which is root on the host (docker).
//
// Each is root-equivalent or reads root's secrets: disk writes the raw
// block devices, kmem reads the kernel's memory, shadow reads every
// password hash, and libvirt, incus-admin and lxd start machines and
// containers as root with the host's disks in reach.
var PrivilegedGroups = []string{"root", "sudo", "wheel", "admin", "adm", "docker", "lxd", "systemd-journal",
	"disk", "shadow", "kmem", "libvirt", "incus-admin"}

// MinRootlessEngine is the oldest Docker Engine admit accepts: from 29.5 a
// rootless container with host networking shares the real host's network
// namespace, which every infrastructure container needs to bind the mesh
// address (Docker's rootless troubleshooting page, read 2026-10-15; spec,
// Observed R1).
const MinRootlessEngine = "29.5.0"

// MeshDropInPath is the user's drop-in that orders its manager after the
// mesh. Its path holds the user's UID, which the probe learns, so the probe
// reads it from its own list (userfiles) rather than from probedFiles.
// doctor asks whether it is there, as the user: it is mode 644.
func MeshDropInPath(uid string) string {
	return "/etc/systemd/system/user@" + uid + ".service.d/paisans-mesh.conf"
}
