# Operator Invites Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A signed invite and a signed request replace every hand step of adding an operator: the age key, `.sops.yaml`, `sops updatekeys` and the root hash.

**Architecture:** A new package `internal/operatorinvite` owns the two file formats, their checks and their SSH signatures, which go through `ssh-keygen -Y` as the config store's signatures do. The commands live in `cmd/paisans`: `operator invite`, `operator age-key`, `operator register --invite`, `operator add --request`, and the join run of `operator register`. The join reuses recovery and attestation, which this plan extracts from `runDeploymentRecover` and `runOperatorRegister` into functions. Making and finding the age key moves out of `ensureAgeKey` into one function that `init`, `age-key` and `register --invite` share.

**Tech Stack:** Go, `filippo.io/age`, `github.com/getsops/sops/v3` (already used), `ssh-keygen` (already required), `gopkg.in/yaml.v3`.

**Spec:** `docs/specs/2026-10-24-operator-invite.md`

## Global Constraints

- Starts after `docs/superpowers/plans/2026-10-24-drop-majority-prompt.md` has merged. Rebase `feat/operator-invite` on `origin/develop` first. `gatherAt` and `configstore.GatherAnchored` then take no `configured` argument.
- Worktree `.worktrees/operator-invite`, branch `feat/operator-invite`. Never use `git stash`.
- Signature namespaces, verbatim: `paisans-operator-invite` and `paisans-operator-request`.
- File format version `1` for both. Any other version is refused.
- Keystore service `paisans-age`, account = the deployment id (`keystore.Service`).
- Never print an age private key or put one in an argument list. Never replace a keystore item or an existing key file.
- Say "blueprint", never "kind". Docs describe current state, with no rejected alternatives.
- The `paisans-cli` CI checks only the changelog: run `go build ./... && go vet ./... && gofmt -l . && go test ./...` before each push.
- PR titles and bodies never mention Outline pages or tickets. Each finding from review is fixed before merge.

## Review Focus

1. **A request whose embedded invite was edited after signing:** refused at `add --request`, naming the invite. Test in Task 2 (`TestRequestRefusesTamperedInvite`).
2. **`register --invite` run twice with the same invite:** the same age public key both times, and no second keystore item. Test in Task 4.
3. **`add --request` on a workstation whose age key opens no recipient of the secrets file:** refused before `paisans-stack.yaml` is written. Test in Task 5.
4. **The join run before the inviter applied** (the sites refuse the key, or the head does not list the operator): refused with no files written, telling them to ask the inviter whether they have applied. Test in Task 6.
5. **An invite for one deployment answered in a directory already holding another deployment's files:** refused, naming `--config`. Test in Task 4.

---

### Task 1: One way to make or find the age key, and `operator age-key`

**Files:**
- Create: `cmd/paisans/operatoragekey.go`, `cmd/paisans/operatoragekey_test.go`
- Modify: `cmd/paisans/agekey.go` (split `ensureAgeKey`), `cmd/paisans/operatorregister.go` (`runOperator` dispatch and its subcommand list)

**Interfaces:**
- Produces:
  - `type ageKey struct { Recipient string; Source string; Made bool }`. `Source` is how the step names where the key came from: `"from SOPS_AGE_KEY_CMD"`, the keystore item's `Describe(id)`, or the file's short path.
  - `func operatorAgeKey(r ui.Reporter, id, configPath, keyFile string) (ageKey, error)` takes the sources in the spec's order: a `SOPS_AGE_*` variable, then the keystore item for `id`, then `keyFile` (read when it exists, made when it does not), then a new keystore item. It never writes `.sops.yaml`.
  - `func runOperatorAgeKey(args []string, stdout io.Writer) error`, flags `--id`, `--config`, `--age-private-key-file`. It prints the recipient alone on stdout as the last line.
- Consumes: `config.AgeVariablesSet`, `config.ReadVariableKey`, `config.AgeKeystore`, `storeKey`, `writeKeyFile`.

- [ ] **Step 1: Write the failing tests** in `operatoragekey_test.go`, with `config.AgeKeystore` replaced by an in-memory `keystore.Store` as the existing `agekey` tests do:
  - `TestAgeKeyMakesThenShows`: the first run's last stdout line matches `^age1[0-9a-z]+$`; a second run prints the same line; the store holds one item.
  - `TestAgeKeyVariableWins`: with `SOPS_AGE_KEY` set to a key and a different keystore item present, it prints the variable's recipient, and the output contains `from SOPS_AGE_KEY`.
  - `TestAgeKeyReadsExistingFile`: `--age-private-key-file` naming an existing file holding one `AGE-SECRET-KEY-1…` prints that key's recipient, and the file is unchanged.
  - `TestAgeKeyIDFromConfig`: with no `--id`, the id comes from `paisans-stack.yaml` beside it; with neither, it is refused, naming `--id`.
  - `TestAgeKeyNeverPrintsPrivate`: stdout and stderr contain no `AGE-SECRET-KEY`.
  - `TestInitStillMakesKeyAndSOPS`: the existing `init` age-key tests pass unchanged.
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./cmd/paisans/ -run 'AgeKey|Init'`. Expected: FAIL, `runOperatorAgeKey` undefined.
- [ ] **Step 3: Implement.** Move key making out of `ensureAgeKey` into `operatorAgeKey`. `ensureAgeKey` keeps its guard (no secrets file and no `.sops.yaml`) and its warnings, calls `operatorAgeKey`, then `config.WriteSOPSConfig`. Recipient from a stored key: `age.ParseX25519Identity(key).Recipient().String()`. Add `"age-key"` to `runOperator` and to both "Use add, …" messages.
- [ ] **Step 4: Run.** Run: `go test ./cmd/paisans/ -run 'AgeKey|Init|Create'`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator age-key makes or shows the operator's age key"`

### Task 2: `internal/operatorinvite`: formats, checks, signatures

**Files:**
- Create: `internal/operatorinvite/invite.go`, `request.go`, `sign.go`, `invite_test.go`, `request_test.go`, `sign_test.go`

**Interfaces:**
- Produces:
  ```go
  const Version = 1
  const InviteNamespace = "paisans-operator-invite"
  const RequestNamespace = "paisans-operator-request"

  type Site struct { Name, Host, User string; Port int }   // yaml: name, host, user, port
  type Inviter struct { Name, Fingerprint string }          // yaml: name, fingerprint
  type Invite struct {
      Version int; Deployment, Domain, Root string
      Inviter Inviter; Username, Key string; Sites []Site
  }
  type Request struct {
      Version int
      Invite  string // the signed invite, whole, as text
      Name, Email, Age string
  }

  func (i Invite) Check() []string             // every problem, each naming its field
  func (q Request) Check() []string
  func EncodeInvite(i Invite) []byte           // stable bytes: same Invite, same bytes
  func EncodeRequest(q Request) []byte
  func ParseInvite(signed []byte, inviterKey string) (Invite, error)  // checks shape, version, signature, and that inviterKey's fingerprint == Inviter.Fingerprint
  func ParseRequest(signed []byte) (Request, Invite, error)          // checks the request's signature against its invite's Key; the invite is parsed for shape only
  func InviteSigner(signed []byte) (Invite, string, error)           // the invite and the public key line that signed it, for add --request to match against the operators

  var Sign = func(keyLine, namespace string, data []byte) ([]byte, error)  // the armored SSH signature; tests replace it
  func Signed(body, sig []byte) []byte                             // body, then the armored block
  func Verify(keyLine, namespace string, body, sig []byte) error
  ```
- `Key` is the canonical key line: type and base64 with no comment, refused otherwise, like `internal/admission`'s `canonicalKey`. Copy that function into this package rather than exporting it.

- [ ] **Step 1: Write the failing tests:**
  - `TestInviteRoundTrip`: `ParseInvite(Signed(EncodeInvite(i), sig), inviterPub)` returns `i`.
  - `TestInviteRefusesWrongInviterKey`, `TestInviteRefusesTamperedByte`, `TestInviteRefusesVersion2`, and `TestInviteRefusesKeyWithComment` / `KeyWithOptions`.
  - `TestInviteCheckNamesEachField`: a bad deployment id, a root that is not 40 hex characters, a username `config.ValidOperatorName` refuses, no sites, a site port 0. Each problem starts with its field name.
  - `TestRequestRoundTrip`; `TestRequestRefusesSignatureByOtherKey`; `TestRequestRefusesTamperedInvite` (one byte of the embedded invite changed, the request re-signed by the invited key); `TestRequestRefusesBadAge` (not `^age1[0-9a-z]+$`); `TestRequestRefusesBadEmail`.
  - `TestVerifyMatchesSSHKeygen`: a signature from the real `Sign` verifies with `ssh-keygen -Y verify`. Skip when there is no `ssh-keygen` or no agent socket.

  Tests make keys with `ssh-keygen -t ed25519 -N '' -f`, as `internal/configstore/local_test.go` does, and replace `Sign` with a function that signs from the private key file.
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./internal/operatorinvite/`. Expected: FAIL, package has no Go files.
- [ ] **Step 3: Implement.**
  - `Sign` writes the key line to a temp file and runs `ssh-keygen -Y sign -n <ns> -f <tmp.pub> -U`, the data on stdin and the signature on stdout, so the agent signs. That is git's `key::` path, which `configSignWith` already relies on.
  - `Verify` writes a temp allowed-signers line, `paisans namespaces="<ns>" <keyLine>`, and runs `ssh-keygen -Y verify -f <file> -I paisans -n <ns> -s <sigfile>` with the body on stdin.
  - `Signed` joins the body and `-----BEGIN SSH SIGNATURE-----…`. The parsers split at the first line that equals `-----BEGIN SSH SIGNATURE-----`.
  - YAML is encoded with fields in table order and parsed with `KnownFields(true)`.
- [ ] **Step 4: Run.** Run: `go test ./internal/operatorinvite/`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator invite and request formats, signed with ssh-keygen"`

### Task 3: `paisans operator invite`

**Files:**
- Create: `cmd/paisans/operatorinvite.go`, `cmd/paisans/operatorinvite_test.go`
- Modify: `cmd/paisans/operatorregister.go` (dispatch)

**Interfaces:**
- Consumes: `operatorinvite.Invite`, `EncodeInvite`, `Sign`, `Signed`; `signingKeyFor(l, cfg)`; `create.CheckNewOperator` for the username and key; `configTransports` or `cfg.Sites` for host, port and `SSHUser()`.
- Produces: `func runOperatorInvite(args []string, stdout io.Writer) error`. Flags: `--key` (file or key line, required), `--config`, one positional username. The signed invite goes to stdout.
- Root: `l.Root()` if set, else the single root of `configstore.GatherAnchored(l, nil, "", anchor, GatherOptions{})` over the local store (`g.Roots(head)`). Refused, naming `deployment pull`, when neither gives exactly one.

- [ ] **Step 1: Write the failing tests:**
  - `TestInviteWritesSignedInvite`: on a registered workstation (`storedHistoryKey` + `registerWorkstation`), `ParseInvite(out, pub)` gives the deployment id, the root `shas[0]`, `Username` `carol`, the key without its comment, and one `Site` per declared site with its `SSHUser()`.
  - `TestInviteRefusesDeclaredUsername`, `TestInviteRefusesTakenKey`, `TestInviteRefusesNoSigningKey` (the message names `operator register`), `TestInviteRefusesUnknownRoot` (it names `deployment pull`).
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./cmd/paisans/ -run TestInvite`. Expected: FAIL.
- [ ] **Step 3: Implement** `runOperatorInvite` and add `"invite"` to the dispatch and the subcommand lists.
- [ ] **Step 4: Run.** Run: `go test ./cmd/paisans/ -run 'TestInvite|TestOperator'`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator invite writes a signed invite"`

### Task 4: `operator register --invite`: answering

**Files:**
- Create: `cmd/paisans/operatoranswer.go`, `cmd/paisans/operatoranswer_test.go`
- Modify: `cmd/paisans/operatorregister.go`: `--invite`, `--inviter-key`, `--name`, `--email` and `--age-private-key-file` flags; with `--invite` it calls `answerInvite` and returns

**Interfaces:**
- Consumes: `operatorinvite.ParseInvite`, `EncodeRequest`, `Sign`, `Signed`; `operatorAgeKey` (Task 1); `agentKeys`, `agentHolds`; `create.Prompter`, plus the full-name and email checks `AskNewOperator` uses (`checkFullName`, `checkEmail` in `internal/create`, which this task exports as `create.CheckFullName` and `create.CheckEmail`).
- Produces:
  - `func answerInvite(r ui.Reporter, dir string, invite []byte, inviterKey, name, email, keyFile string, stdin io.Reader, stdout io.Writer) error`
  - The recorded files `<dir>/.paisans/invite` (the signed invite, verbatim) and `<dir>/.paisans/inviter.pub` (the inviter's key line), mode 0600, in a directory created 0700. Task 6 reads them with `func readJoin(dir string) (invite []byte, inviterKey string, ok bool, err error)`, defined here.

- [ ] **Step 1: Write the failing tests:**
  - `TestAnswerWritesSignedRequest`: `ParseRequest(out)` returns the typed name and email, an `age1…` recipient equal to `operatorAgeKey`'s, and the invite verbatim. `.paisans/invite` equals the input.
  - `TestAnswerTwiceSameAgeKey` (Review Focus 2): two runs give the same `Age`, and the in-memory keystore holds one item.
  - `TestAnswerRefusesWrongInviterKey`: nothing is written, and no keystore item is made.
  - `TestAnswerRefusesKeyNotInAgent`: with `agentKeys` replaced to return another key, the message names `ssh-add`.
  - `TestAnswerRefusesOtherDeploymentHere` (Review Focus 5): a directory whose `paisans-stack.yaml` has another id is refused, naming `--config`.
  - `TestAnswerWithoutTerminalNeedsNameAndEmail`: with no terminal, a missing `--name` or `--email` is refused, naming the flag.
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./cmd/paisans/ -run TestAnswer`. Expected: FAIL.
- [ ] **Step 3: Implement**, in the spec's order: check the invite, check the agent, make or find the age key, ask, sign, record, print. Make the age key only after both checks, so a refused invite makes no key.
- [ ] **Step 4: Run.** Run: `go test ./cmd/paisans/ -run 'TestAnswer|TestOperatorRegister'`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator register --invite answers an invite with a signed request"`

### Task 5: `operator add --request`: accepting

**Files:**
- Modify: `cmd/paisans/operatoradd.go`, `cmd/paisans/operatoradd_test.go`
- Create: `internal/config/sops_recipients.go` and its test

**Interfaces:**
- Consumes: `operatorinvite.ParseRequest`, `InviteSigner`; `config.Recipients`, `config.LoadDeploymentSecrets`, `config.WriteSecretsBytes`, `config.AddOperatorBytes`, `config.WriteDeclaration`; the existing site questions in `create.AskNewOperator` (called with ID, name, email and keys filled in, so it asks only for sites).
- Produces: `func AddSOPSRecipient(dir, recipient string) ([]byte, error)`. It returns `.sops.yaml` with `recipient` appended to the `age` field of every creation rule, comma-separated, and the rest of the file byte-for-byte unchanged. When the recipient is already present it returns the file unchanged. It edits the YAML node tree, not the text.

- [ ] **Step 1: Write the failing tests:**
  - `TestAddSOPSRecipient*`: one rule; two rules; already present; a missing file is refused.
  - `TestOperatorAddRequestDeclaresAndReencrypts`: on a deployment fixture with real age keys (use `filippo.io/age` to make two identities, put the inviter's in the keystore, and write secrets with `WriteSecrets`):
    - `operators.carol` has the request's name, email and key;
    - `config.Recipients` names both age keys;
    - `LoadDeploymentSecrets` opens the file with each identity.
  - `TestOperatorAddRequestTwiceIsNoOp`: the second run changes no byte of the three files and says so.
  - `TestOperatorAddRequestReencryptsOnly`: carol is declared with the same key, but her age key is absent. Only `.sops.yaml` and the secrets change.
  - `TestOperatorAddRequestRefusesInviteFromNonOperator`, `…OtherDeployment`, `…OtherRoot`, `…DeclaredWithOtherKey`, and `…WithNameFlag` (`--name` beside `--request`).
  - `TestOperatorAddRequestCannotDecrypt` (Review Focus 3): with no usable age key, it is refused, and `paisans-stack.yaml` is unchanged.
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./cmd/paisans/ -run OperatorAddRequest ./internal/config/ -run SOPSRecipient`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Do every check and compute all three new contents in memory before writing anything: the declaration bytes, the `.sops.yaml` bytes, and the secrets bytes from `WriteSecretsBytes` into a temp path.
  - Write `.sops.yaml` and the secrets file through a temp file plus rename, then `paisans-stack.yaml` with `WriteDeclaration`.
  - If a later write fails, restore the files already written from the bytes read at the start.
  - The `Next:` lines say `deployment apply --execute`, then that carol runs `operator register --execute` in the directory where she answered.
- [ ] **Step 4: Run.** Run: `go test ./cmd/paisans/ -run OperatorAdd ./internal/config/`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator add --request declares the operator and re-encrypts the secrets to them"`

### Task 6: `operator register`, joining

**Files:**
- Modify: `cmd/paisans/deployment.go`: extract from `runDeploymentRecover`, everything after flag parsing, into `recoverFiles`
- Modify: `cmd/paisans/operatorregister.go`: extract the attestation half of `runOperatorRegister` into `attest`; add the join branch
- Create: `cmd/paisans/operatorjoin.go`, `cmd/paisans/operatorjoin_test.go`

**Interfaces:**
- Produces:
  - `func recoverFiles(r ui.Reporter, l configstore.Local, dests []config.Destination, a configstore.Anchor, id string, held map[string]string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer, accept func(head *config.Config) error) error`. `accept` runs on the head's configuration before anything is written, and its error is returned as is. `runDeploymentRecover` passes `nil`.
  - `func attest(r ui.Reporter, l configstore.Local, cfg *config.Config, key, pin string, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) error`, which is the body of `runOperatorRegister` from the gather onward.
  - `func joinInvite(r ui.Reporter, l configstore.Local, invite operatorinvite.Invite, execute bool, ans answerer, stdin io.Reader, stdout io.Writer) error`
- Join branch: `runOperatorRegister` with no `--invite`, where `!l.HasFiles()` and `readJoin(l.Dir)` is `ok`. The invite is re-checked with `ParseInvite(invite, inviterKey)`. The destinations come from `invite.Sites` (`config.Destination{User, Host, Port}`), the anchor is `configstore.Anchor{Root: invite.Root}`, and `held` is the invite's key.

- [ ] **Step 1: Write the failing tests** in `operatorjoin_test.go`, built on the recovery test harness (`storedHistoryKey`, `recoverHosts`):
  - `TestJoinBeforeApplyRefused` (Review Focus 4): the head does not list carol. Refused, the message says to ask the inviter whether they have applied, and the directory holds no `paisans-stack.yaml` and no `.paisans/config-head`.
  - `TestJoinRecoversAndAttests`: the head lists carol's key. With `--execute`, the three files are written, `.paisans/config-root` is the invite's root, the signing key is carol's, and an attestation is pushed.
  - `TestJoinNeverTrustsOtherRoot`: the sites serve a history with another root. Refused.
  - `TestJoinSaysWhenSecretsDoNotOpen`: the secrets are not encrypted to carol's age key. It succeeds, and the output reports a failed check naming `operator add --request`.
  - `TestJoinDryRunWritesNothing`.
  - `TestRecoverUnchanged`: the existing `deployment recover` tests pass unchanged.
- [ ] **Step 2: Run them to see them fail.** Run: `go test ./cmd/paisans/ -run 'TestJoin|Recover|OperatorRegister'`. Expected: FAIL.
- [ ] **Step 3: Implement**, extracting first. Run the recover and register tests green after each extraction, before adding `joinInvite`. The `accept` passed by `joinInvite` checks that `head.Operators[invite.Username].KeyLines()` contains `invite.Key`. A login refusal from every site maps to the same "ask the inviter" problem. The secrets check uses `config.LoadDeploymentSecrets(path, invite.Deployment)`, reported as a `ui.Failed` step, not an error.
- [ ] **Step 4: Run.** Run: `go test ./cmd/paisans/`. Expected: PASS.
- [ ] **Step 5: Commit.** `git commit -m "feat: operator register joins from a recorded invite"`

### Task 7: Docs, help, changelog, and PR

**Files:**
- Modify: `README.md` (*Operators and community admins*, the `operator add` paragraph near :546-555)
- Modify: `docs/commands.md` (new sections `paisans operator invite` and `paisans operator age-key`; `operator register` and `operator add` describe `--invite` and `--request`)
- Modify: `cmd/paisans/help.go`, `CHANGELOG.md`

- [ ] **Step 1: Write the docs.**
  - The README paragraph describes the five-step flow from the spec in present tense.
  - Each `commands.md` section follows the existing *Alone / Flags / Hosts / `--execute`* bullet shape.
  - `help.go` gets entries for `operator invite` and `operator age-key`, and its `operator register` and `operator add` entries name the new flags.
- [ ] **Step 2: Add the changelog bullets** under `### Added`, each at most 35 words:
  ```
  - `operator invite` writes a signed invite, `operator register --invite` answers it with a signed request and the operator's age key, and `operator add --request` declares them and re-encrypts the secrets.
  - `operator register` beside a recorded invite recovers the deployment from the invite's root and signs the operator's attestation.
  - `operator age-key` makes or shows the operator's age key for a deployment.
  ```
- [ ] **Step 3: Verify.** Run: `go build ./... && go vet ./... && gofmt -l . && go test ./...`. Expected: no gofmt output, and every package `ok`.
- [ ] **Step 4: Commit, push, and open the PR** against `develop`, with the `🤖 Generated with [Claude Code](https://claude.com/claude-code)` footer.

### Task 8: Staging run

Two workstations, one deployment on the staging hosts. Claude may admit and apply there.

- [ ] **Step 1:** Run the five steps of the spec's flow, with the second workstation as a fresh `HOME` holding its own SSH key, in its own agent.
- [ ] **Step 2:** Check the result:
  - the new operator's `deployment pull` succeeds;
  - `LoadDeploymentSecrets` opens on their workstation;
  - `ssh paisans-<token>@<site>` works with their key;
  - Pocket ID has their account.
- [ ] **Step 3:** Report each step's output, with any refusal quoted exactly.
