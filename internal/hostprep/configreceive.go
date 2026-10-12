package hostprep

// ConfigVersionsShell and ConfigReceiveShell define the shell functions a
// site's store is read and written through, written once here for every
// place that runs them: the sync script, and the push and the pull that
// configstore sends over ssh. ConfigReceiveShell includes
// ConfigVersionsShell, which it uses; a pull, which only reads, sends
// ConfigVersionsShell alone.
//
// A version is a ref refs/versions/<sha> that names the commit it points to.
// paisans_versions REPO PREFIX prints the hash of every ref PREFIX<sha> that
// is one: a commit, a full lowercase hash, and named for itself. Every other
// ref is left out, so a name holding a quote, or a ref moved to another
// commit, never reaches a command line or a bundle.
//
// paisans_receive REPO SOURCE [SECONDS] takes the versions SOURCE holds into
// REPO. git fetch would fast-forward refs/versions/<x> to a child of x even
// without a plus in the refspec, which hides x and makes every later honest
// fetch of x a non-fast-forward that fails as a whole. So nothing is fetched
// into refs/versions/: the fetch lands in a quarantine of its own,
// refs/incoming/<time>.<pid>/, and each correctly named version there is
// created only where it is absent, against the null hash, so an existing ref
// never moves. Each ref is created on its own, so one that fails leaves the
// rest. The quarantine is deleted afterwards. SECONDS bounds the fetch with
// timeout. It returns the fetch's own status.
const ConfigVersionsShell = `paisans_versions() {
	git -C "$1" for-each-ref --format='%(objectname) %(objecttype) %(refname)' "$2" |
	while read -r _pv_oid _pv_type _pv_name; do
		case $_pv_oid in *[!0-9a-f]*) continue ;; esac
		[ ${#_pv_oid} -eq 40 ] || [ ${#_pv_oid} -eq 64 ] || continue
		[ "$_pv_type" = commit ] && [ "$_pv_name" = "$2$_pv_oid" ] || continue
		echo "$_pv_oid"
	done
}
`

// ConfigReceiveShell is ConfigVersionsShell and paisans_receive.
const ConfigReceiveShell = ConfigVersionsShell + `paisans_receive() {
	_pr_q="refs/incoming/$(date +%s).$$"
	if [ -n "${3:-}" ]; then
		timeout "$3" git -C "$1" fetch --no-prune --no-tags --quiet "$2" "+refs/versions/*:$_pr_q/*"
	else
		git -C "$1" fetch --no-prune --no-tags --quiet "$2" "+refs/versions/*:$_pr_q/*"
	fi
	_pr_rc=$?
	case $(git -C "$1" rev-parse --show-object-format) in
	sha256) _pr_null=0000000000000000000000000000000000000000000000000000000000000000 ;;
	*) _pr_null=0000000000000000000000000000000000000000 ;;
	esac
	paisans_versions "$1" "$_pr_q/" | while read -r _pr_oid; do
		git -C "$1" update-ref "refs/versions/$_pr_oid" "$_pr_oid" "$_pr_null" 2>/dev/null || :
	done
	git -C "$1" for-each-ref --format='delete %(refname)' "$_pr_q/" | git -C "$1" update-ref --stdin
	return $_pr_rc
}
`
