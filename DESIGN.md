# gitforgery — Design & Architecture

Status: proposal (draft v1) · Repo: `sauryagur/gitforgery` (Go, Bubble Tea template)

## 1. Vision

`gitforgery` is a Go CLI + Terminal UI for **crafting git history**, not just filtering it:

1. **Forge (rewrite)** — apply a declarative _forgery recipe_ to an existing repo's history:
   author/committer identities, dates, messages, per-commit or by rules.
2. **Fabricate (generate)** — synthesize a plausible history from nothing (demo repos,
   decoy/honeypot repos, migration-tool load tests).
3. **Audit (detect)** — scan a repo for the residue a rewrite or crafted history leaves behind.

Positioning: `git filter-repo` (the current standard) is a _content_ filter —
strip blobs, paths, refs. `gitforgery` targets **identity/date/message fabrication**
with a repeatable recipe format, a preview-first workflow, and a TUI. The template's
Bubble Tea stack is the TUI; the core rewrite is plumbing, not spinners.

The repo today is the stock `bubbletea-app-template` (module
`github.com/charmbracelet/bubbletea-app-template`, bubbles v1.0.0 / bubbletea v1.3.10 /
lipgloss v1.1.0). Milestone M0 renames the module and replaces the demo app.

## 2. What "forgery" means here (honest framing)

Git's commit object is: `tree`, `parent(s)`, author, committer, optional
signature/encoding headers, message. Nothing in the object binds the author/committer
fields to a real-world identity — anyone can write any identity and any date.
Git _never validates_ identities, and only the committer date is "now" by
convention, not by enforcement (`GIT_COMMITTER_DATE`, `GIT_AUTHOR_DATE`,
`git commit --date` are trusted inputs). That is the forge surface we automate.

Boundaries — what this tool does **not** and cannot do:

- **Signatures cannot be forged.** A `gpgsig` (OpenPGP/X.509/SSH) header signs the
  commit bytes; any rewrite invalidates it. The tool strips invalid signatures and may
  _re-sign_ with the user's own key, which is just signing.
- **SHA-1 collisions are out of scope for _creating_**: real chosen-prefix collisions
  cost ~$45k of compute (Karpman–Leurent–Peyrin, _SHA-1 is a Shambles_, USENIX 2020;
  the original 2017 SHAttered demonstration cost more). Git itself ships collision
  detection (sha1dc) and refuses to operate on colliding objects. We instead ship an
  **audit** mode that detects the conditions a collision attack needs.
- **Detection is always possible out-of-band.** Even a perfect object-level rewrite
  leaves: reflog entries (old SHAs), unreachable old objects until `gc --prune`,
  invalidated signatures, GitHub "Verified" badge absence, commit-graph state,
  committer-after-author-date anomalies. The tool documents these in `--help` and in
  `audit`, which reports them.

Legitimate uses: GDPR/identity scrubbing, demo repositories, honeypots for
supply-chain research, testing migration tools, teaching. The tool is not a fraud
instrument — see §11.

## 3. Core mechanics (verified facts)

### 3.1 Objects are content-addressed; rewriting cascades

A commit's SHA-1 (or SHA-256) is the hash of its serialized bytes, including parent
hashes. Changing author/date/message on one commit changes its hash and every
descendant's hash. Trees and blobs are untouched by metadata-only rewrites — only
commit objects are re-created. This is why a metadata-only rewrite is cheap: the same
tree/blob OIDs are reused.

### 3.2 The sanctioned rewrite transport: `fast-export` → transform → `fast-import`

From `git-fast-export(1)` (docs for git 2.51, which is current):

> You can use it … as a format that can be edited before being fed to
> `git fast-import` in order to do history rewrites (an ability relied on by tools
> like _git filter-repo_).

The stream is a line/verb format (`blob`, `commit`, `tag`, `reset`, …) containing
author/committer lines with full timestamps, so **date and identity rewriting is a
pure text transform on the stream**. Options we rely on (all verified in the man
page):

| Option                              | Purpose                                                                                         |
| ----------------------------------- | ----------------------------------------------------------------------------------------------- |
| `--show-original-ids`               | emits `original-oid <sha>` per commit/blob → old→new mapping without marks                      |
| `--export-marks` / `--import-marks` | persistent mark table (old sha → `:mark`); enables incremental & mapping                        |
| `--reference-excluded-parents`      | partial range (`main~10..main`) without rewriting ancestors — parents referenced by sha         |
| `--no-data`                         | skip blobs entirely; only valid if target already has the objects (fast partial rewrites)       |
| `--signed-commits=strip`            | default for commits — signatures are invalid after any edit                                     |
| `--signed-tags=abort`               | default for tags — must be overridden (`strip`) for tag-bearing repos                           |
| `--reencode=yes/no/abort`           | message `encoding` header handling; default `abort` on non-UTF-8 commits                        |
| `--use-done-feature`                | well-formed `feature done` … `done` stream                                                      |
| `--anonymize`                       | git's own built-in anonymizer (bug-report use); a lower bound for our `audit`/`anonymize` later |

Known limitation (from the man page): fast-import cannot tag trees (e.g. linux.git) —
edge case we flag, not fix.

### 3.3 Re-signing

After import, `git fast-import` stores at most one signature per hash algorithm
(both old and new `gpgsig` stream forms exist). Re-signing is a separate, interactive
step (`git commit --amend -S`, `git tag -s`) because it may prompt for a passphrase —
the core rewrite stays non-interactive; `gitforgery re-sign` is a follow-up subcommand.

### 3.4 Library options (Go ecosystem)

| Approach                              | Pros                                                                                                                            | Cons                                                                                                                                                                                                                                                  |
| ------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **git CLI pipeline** (chosen)         | byte-for-byte faithful to git; handles SHA-256 repos natively; zero ODB divergence; git is a sensible dependency for a git tool | requires `git` ≥ 2.45 (for `--signed-commits`)                                                                                                                                                                                                        |
| **go-git** (planning/inspection only) | pure Go; read commit metadata, resolve revs, build DAGs, compute preview hashes                                                 | no SHA-256 repo support (confirmed broken for go-git-using tools, e.g. gitui/Gitnuro); no sha1dc unless v5.4+ sha1cd default — use `plumbing/hash` default, never the native `sha1.New` fallback (go-git docs: only safe against trustworthy servers) |
| **libgit2/git2go**                    | faithful; sha1dc; sha256 support recent                                                                                         | CGO — worse builds/releases; little gain over the CLI for a CLI tool                                                                                                                                                                                  |

Decision: **git CLI pipeline for execution, go-git for inspection/planning.**
If "no git binary" ever becomes a requirement, the `stream` package (§5) is written
against our own parser so a pure-Go fast-import emitter can replace the CLI later
(big job; see Open Questions).

## 4. CLI surface

```
gitforgery plan   --recipe r.yaml [--repo .] [--range …]
    # Read-only: resolve range, match rules, print old→new hash table + diff stats.
    # Exit 0 with no changes if recipe is a no-op.

gitforgery apply  --recipe r.yaml [--repo .] [--to out.git | --in-place]
    [--backup refs/forgery/original] [--yes] [--force] [--signed-tags=strip]
        # Export → transform → import → map refs → backed-up update → verify.


gitforgery forge  [--from HEAD] [--plan plan.yaml] [--schedule sched.yaml]
    [--author "Ada Lovelace <ada@example.com>"] [--to out.git | --in-place]
    [--yes] [--force]
    # Decompose: replace `--from` (one tip commit, default HEAD) with an ordered
    # sequence of commits per the agent-authored plan (§13.1), dates per the
    # ScheduleSpec scheduler (§13.2), via the stream splitter (§13.3).
    # Export → redistribute M/D ops → import → verify (final tree equality).

gitforgery undo  [--repo .] [--ref refs/heads/…] [--yes]
    # Restore the most recent refs/forgery/original backup from an --in-place run.

gitforgery fabricate --recipe demo.yaml --out repo.git [--seed N]
    # Create a brand-new repo with synthetic history (fresh `git init`, fast-import).

gitforgery audit  [--repo .] [--full]
    # Forgery-indicator scan (§9). Read-only.

gitforgery re-sign [--repo .] [--gpg-key KEY] [--ssh-key PATH]
    # Re-sign commits/tags after an apply that stripped signatures. Interactive.

gitforgery tui   [--recipe r.yaml]
    # Interactive recipe editor + preview + apply (§8).
```

Bare `gitforgery` prints help; no hidden mutate-by-default behavior. Every mutating
command requires `--yes` or a confirming TUI keystroke unless `--force`.

## 5. Architecture

```
cmd/gitforgery/            # cobra root; thin
internal/recipe/           # YAML recipe: parse, validate, render docs
internal/plan/             # go-git reads: resolve range → DAG → match rules → Plan
internal/stream/           # fast-export/fast-import stream vocabulary
   lexer.go                # incremental line/AST reader (large blobs via headers)
   transform.go            # applies plan ops to commit/tag/author/committer lines
   emit.go                 # writes transformed stream (+ original-oid passthrough)
internal/exec/             # os/exec wrappers: export, import, fsck, rev-parse
internal/fab/              # synthetic-history generator (recipe → stream)
internal/audit/            # detection scans (§9)
internal/sign/             # re-sign (gpg/ssh) orchestration
internal/ui/               # Bubble Tea TUI (§8)
internal/gitx/             # small go-git helpers (ident parsing, preview hashing)
```

### 5.1 Data flow (`apply`)

```mermaid
flowchart LR
    A[source repo] -->|"git fast-export --show-original-ids --signed-commits=strip --signed-tags=strip --export-marks"| B[stream]
    B --> C[stream/lexer]
    C --> D[plan: rules matched per commit oid]
    D --> E[stream/transform]
    E --> F[stream/emit]
    F -->|"git fast-import (temp bare repo)"| G[new history]
    G --> H["ref mapping: original-oid/marks → new oids"]
    H --> I["update refs in place (--in-place) or leave in --to"]
    I --> J["verify: git fsck --full; per-commit tree-oid equality"]
    J --> K["backup refs/forgery/original + report"]
```

Preview (`plan`) reuses C + D and computes new commit hashes by re-encoding planned
commit objects in Go (trees unchanged → cheap; exact hash must match what
fast-import will produce, verified by tests). `plan` exits non-zero if the preview
hash disagrees with the post-import reality — a canary for stream-vocabulary drift.

### 5.2 Safety invariants (all modes)

1. Default output is a **new repo** (`--to`); `--in-place` requires explicit opt-in.
2. `--in-place` first creates `refs/forgery/original/<ref>` pointing at pre-rewrite tips.
3. Ref updating uses `git update-ref` per-ref with a recorded transaction log.
4. Dirty worktree → refuse unless `--force` (a rewrite invalidates any working tree
   against the old tips).
5. Post-verify: `git fsck --full` clean, and every rewritten commit's tree OID equals
   its source commit's tree OID for metadata-only recipes (content must be untouched).
6. `--range` partial rewrites use `--reference-excluded-parents`; excluded ancestors
   are **not** rewritten; the stream may then only be imported into a repo that has
   those ancestors (we import into a clone of the source, then update refs).

## 6. Recipe format (YAML)

```yaml
# recipe.yaml — identity/date/message forgery plan
range: "main~10..main" # git rev-list range, or "all" (default: all reachable)
match: # ordered; first match wins per commit
  - when:
      author_email: "@oldcorp\\.com$" # regex on author/committer fields
    set:
      author: "Ada Lovelace <ada@example.com>"
      committer: "inherit" # mirror the rewritten author
      author_date: "2020-01-01T10:00:00+00:00"
      committer_date: "author" # = author_date | "now" | RFC3339 | "first+4h"
      subject: "{{.Subject}}" # text/template over {Subject,Body,Author,Committer,AuthorDate}
      body: "{{.Body}}"
  - when:
      sha: "a1b2c3d4"
    set:
      author_date: "prev+2h" # relative to previous rewritten commit
options:
  strip_signatures: true # always true while re_sign=false (they'd be invalid)
  re_sign: false
  preserve_date_order: true # clamp so committer dates stay monotonic
  refs: ["refs/heads/*", "refs/tags/*", "refs/notes/*"]
  backup_ref: "refs/forgery/original"
```

Design notes:

- `when` selectors: `sha`, `author`, `author_email`, `committer`, `committer_email`,
  `subject`, `branch` (each regex, absent = match-all).
- Value grammar: literals, `inherit`, `author`, `now`, `first±dur`, `prev±dur`,
  templates. No eval — safety and determinism.
- `apply --dry-run` == `plan`; recipes are diffable, reviewable, CI-able.

## 7. Fabricate (synthetic history)

```yaml
# demo.yaml
out: ./demo.git
tree: ./scaffold/ # optional seed directory copied into every/root commit
commits: 120
time: { start: "2023-01-01", end: "2024-06-01", clustering: 0.8 }
authors:
  - { name: "Ada", email: "ada@x.io", weight: 60 }
  - { name: "Bob", email: "bob@x.io", weight: 40 }
branches:
  - { name: "feature/auth", from: "main", commits: 8, merge: true }
messages:
  style: conventional # or custom templates with subject/body pools
seed: 42 # deterministic output
```

Generator: schedules commit times (cluster + weekday/sleep modeling), picks authors
by weight, emits a fast-import stream, imports into `git init --bare`. Deterministic
under `seed`. This is the "contribution graph" and decoy/honeypot use case.

## 8. TUI (Bubble Tea)

Screens (model per screen, `ui/`):

1. **Repo/range** — repo picker `filepicker`; range as textinput validating via
   `git rev-parse`.
2. **Commit table** — `table`: sha, date, author, subject; row select → edit screen.
3. **Identity/date editor** — textinputs for name/email; date via textinput
   (RFC3339, validated live) + a small custom calendar grid. Note: bubbles has **no
   datepicker** component (it's an open feature request, bubbles#404) — the calendar
   is ~100 lines of pure view code; or use `bubbles` v2's `textinput` only.
4. **Preview** — side-by-side old→new table (lipgloss colors), changed-fields diff
   per commit; space toggles apply.
5. **Apply/verify** — `spinner` + `progress` during export/import/verify; result
   summary with backup-ref instructions.

The template pins bubbles **v1** (`github.com/charmbracelet/bubbles v1.0.0`) while
current docs describe v2 (`charm.land/bubbles/v2`, Bubble Tea v2, `tea.KeyPressMsg`).
M0 decides: stay on v1 paths (fine; v1 remains published) or migrate to v2 (churn now,
current API). Recommendation: migrate to v2 at M0 while the codebase is 20 lines.

## 9. Audit (forgery-indicator scan)

Read-only; each check independently reported, severity-ranked:

1. **Rewrite residue**: unreachable objects (`git fsck --unreachable` count) and
   reflog entries whose tips don't match current refs — the universal fingerprint of
   a history rewrite.
2. **Signature state**: any unsigned commits claiming identity, any _recently
   committed_ commits whose gpgsig is stale/invalid.
3. **Date anomalies**: committer_date < author_date; committer dates far before
   object file mtimes; date clustering that matches generator heuristics (flag, low
   confidence).
4. **Collision hygiene**: confirm the local toolchain's SHA-1 is collision-detecting
   (git sha1dc; go-git sha1cd — detect the native `sha1.New` fallback in any vendored
   go-git and flag it); with `--full`, rescan the ODB re-hashing every object and
   report any hash shared by two distinct objects (should never legitimately happen).
5. **Exotic structure**: tags pointing at non-commits, `refs/notes` churn — cheap
   smells of manipulation.

## 10. Milestones

| M   | Scope                                                                                                                           | Acceptance                                                                                                                                                                                                          |
| --- | ------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M0  | Rename module → `github.com/sauryagur/gitforgery`; cobra root; `version`; remove spinner demo; (optional) migrate to bubbles v2 | `go build`, `go vet`, golangci clean; `gitforgery --version`                                                                                                                                                        |
| M1  | `recipe` + `plan`: open repo (go-git), resolve range, match rules, preview table                                                | golden tests on fixture repos with known SHAs; `plan` matches hand-computed expectations                                                                                                                            |
| M2  | `stream` + `exec` + `apply --to` in temp repo; marks/original-oid mapping; `fsck` verify                                        | end-to-end fixture test: rewrite author/dates, tree OIDs unchanged, new repo `fsck` clean                                                                                                                           |
| M3  | `--in-place` + backup refs; tags & annotated tags; partial ranges                                                               | backup restore works; annotated-tag mapping correct                                                                                                                                                                 |
| M4  | TUI (editor, preview, confirm, progress)                                                                                        | scripted TUI test (bubbletea testing harness) applies a recipe                                                                                                                                                      |
| M5  | `fabricate`                                                                                                                     | deterministic under seed; output clones cleanly; fsck clean                                                                                                                                                         |
| M6  | `audit` + `re-sign` + SHA-256-repo handling + README + sample recipes                                                           | audit flags a deliberately forged fixture; sha256 repo gives clear behavior                                                                                                                                         |
| M7  | Polish: goreleaser (already configured), docs site, `--no-data` fast path                                                       | release artifact builds                                                                                                                                                                                             |
| M8  | `forge` v1: Plan schema + validator + stream splitter (single tip-commit) + ScheduleSpec scheduler + `forge --to`               | seeded run decomposes a squashed tip into N commits per plan; final composed tree == original tree (`fsck` clean); golden plan fixtures; scheduler property tests (monotonic, MinGap, active-hours/day constraints) |
| M9  | `forge --in-place` + backup/`undo` + `fabricate` `schedule:` block; Plan JSON Schema artifact                                   | in-place forge backed up then `undo` restores; fabricate deterministic under `schedule:`; schema ships with README entry                                                                                            |

## 11. Security & ethics posture

- The tool does not defeat git's integrity guarantees: it manufactures the _metadata_
  git deliberately leaves forgeable. We state this in `--help` and README.
- **Detectability is a feature**: `audit` exists precisely because rewritten history
  leaves fingerprints. The README will list them so users understand the limits.
- No exfiltration/network behavior; recipes are local files; `re-sign` only ever uses
  the user's own keys (gpg/ssh agent, no key material handling).
- Supply-chain hygiene: minimal deps (cobra, yaml.v3, go-git, bubbletea stack);
  dependabot already enabled on the template's workflows.

## 12. Open questions

1. **SHA-256 repos**: go-git can't read them; `plan` must detect `--object-format`
   and degrade to CLI-only (or refuse with a clear error). Confirm go-git status at
   M1.
2. **bubbles v1 vs v2** at M0 (see §8).
3. **Stream grammar drift**: fast-import stream vocabulary evolves (e.g. new
   `gpgsig <algo> <fmt>` lines in 2.45). Mitigation: `plan` preview-hash canary (§5.1)
   plus golden fixture tests pinned per git version.
4. **Annotated-tag and notes refs** mapping strategy (marks-based; verify fast-export
   covers `refs/notes` under `--all`).
5. Whether `fabricate` should also support grafting onto an existing root
   (`--parent`) — nice for "backdating an existing repo's founding commit" demos;
   defer to M5+.

## References

- `git-fast-export(1)` — https://git-scm.com/docs/git-fast-export (transport, options,
  `--signed-commits` default strip, marks, `--show-original-ids`)
- `git-fast-import(1)` — https://git-scm.com/docs/git-fast-import (stream grammar)
- git-filter-repo — https://github.com/newren/git-filter-repo (precedent; content filtering)
- go-git compatibility notes (collision-detecting hash default vs native sha1) —
  https://deepwiki.com/go-git/go-git/10-compatibility
- bubbles v2 upgrade guide (component inventory, no datepicker) —
  https://github.com/charmbracelet/bubbles/blob/master/UPGRADE_GUIDE_V2.md
- SHA-1 chosen-prefix collisions are practical (~$45k): "SHA-1 is a Shambles",
  Leurent/Peyrin, USENIX Security 2020 — https://sha-mbles.github.io/

## 13. Forge: decompose a squashed tip into organic history (v2 addendum**

A reviewed low-level design (external, Claude-generated) proposed a `CommitSpec` /
`ChangeSet` / `commit-tree` rebuild core (`internal/engine`, `internal/gitcore`). Verdict:
**not adopted wholesale** - it trades the existing single code path (the stream transform,
section 5) for a heavier rebuild core,and drops `audit` / `re-sign` / `fabricate`. Adopted
instead, three narrow slices on the existing pipeline: an agent-first plan format,
a deterministic scheduler,and the split expressed as a stream-transform extension. The
caller supplies the logical commit order (that is the product's point); Go-import-graph
dependency inference is out of scope(section 13.5).

###13.1 Plan format (agent/human input)

JSON or YAML, fed to `forge` via `--plan`:

```json
{
  "commits": [
    {
      "message": "feat: core types",
      "files": ["internal/plan/types.go", "go.mod"]
    },
    { "message": "feat: planner engine", "files": ["internal/plan/*.go"] },
    { "message": "test: planner", "files": ["internal/plan/*_test.go"] }
  ],
  "author": { "name": "Ada Lovelace", "email": "ada@example.com" },
  "schedule": {
    "start": "2026-08-01",
    "end": "2026-09-05",
    "active_hours": [
      ["09:00", "12:30"],
      ["14:00", "18:00"]
    ],
    "burst": "crunch-at-end",
    "seed": 7
  }
}
```

Rules:

- `commits` is **required, ordered**: every path changed by `--from` must match
  exactly one commit's `files`; unmatched or multiply-matched paths fail validation with
  the offender listed. Deleted paths count as changed paths and must be covered..
- `files` are glob patterns (`*`, `**`, `?`), matched in commit order; first match
  wins.(Doublestar for `**`; no `!` negation in v1.)
- Top-level `author` defaults the identity; per-commit `author` / `author_date`
  fields override. `--author` (CLI) overrides everything..
- Timestamps come from `schedule`(section 13.2), applied after bucketing;
  `--schedule` overrides an embedded `schedule:` block. Omitting `schedule` yields
  a plain deterministic split: source dates, each next commit offset by `MinGap`,
  strictly increasing.. The resulting sequence is always strictly non-decreasing
  (no time-travel;v1 has no escape hatch).
- `--from` names ONE existing tip commit (default `HEAD`; must have no descendants —
  interior commits are deferred, section 13.5). The diff `from^..from` is the unit
  the plan redistributes.(A root commit has no parent — the root diff is the whole
  tree,andthat works.)
- Validation runs automatically before applying(coverage, uniqueness, monotonic
  dates, non-empty buckets);the same validator feeds preview and execution — no drift.

###13.2 Scheduler (`ScheduleSpec`)

```go
type ScheduleSpec struct {
    Start, End    time.Time        // default: from's parent author_date -14d ... now
    Timezone      *time.Location   // default: local
    ActiveHours    []HourRange      // e.g. [[09:00,12:30],[14:00,18:00]]; default 09-18
    ActiveDays    []time.Weekday  // default: Mon-Fri
    Gaps          []DateRange        // simulated days-off
    BurstProfile   string             // steady | front-loaded | crunch-at-end | random (default: steady)
    Seed          int64              // 0 = time-seeded (non-reproducible); >0 = deterministic
    MinGap      time.Duration       // default 5m; min spacing between consecutive commits
    CommitDateOffset Max             // default 0: random CommitDate offset in [0,Max], seeded,
                                      // emulatinga later amend/rebase — off by default for test predictability
}
```

Algorithm sketch:

1. Distribute N commits across eligible days (`Start..End`, minus `Gaps`, filtered by
   `ActiveDays`) by `BurstProfile` weight (e.g. crunch-at-end skews density toward
   the last third; front-loaded toward the first).
2. Within a day, place commits in `ActiveHours` via Poisson-like inter-arrival —
   evenly spaced stampsare themselves a forgery smell — clamped by `MinGap`.
3. Enforce global monotonic non-decreasing order across the whole sequence..
4. `CommitDate` = `AuthorDate` + seeded offset in [0, `CommitDateOffset.Max`];
   default off(this answers the LLD's open question firmly: default OFF, opt-in.)

One implementation serves `fabricate` too — section 7 gains an optional `schedule:`
block that overrides the ad-hoc `time:` / `clustering` fields.. One scheduler, two users.

###13.3 Stream splitter (mechanics; extension of `internal/stream/transform.go`)

No new pipeline — same export → transform → import → verify path as `apply`:

1. Export `from^..from` with marks;; blobs stay intact andreusable across buckets..
2. The transform buckets each `M` / `D` file-op line into the first plan commit whose
   `files` matches its path.. The unit is the per-commit incremental op set, as
   fast-export already emits it — blob marks refer to bytes already in the stream,
   so no re-hashing and no temp worktree are needed..
3. Emit N `commit` records: bucket[1] = parent(s) + bucket[1] ops;; bucket[k] =
   bucket[k-1] + bucket[k] ops.. Author/committer/date per plan + scheduler..
4. Re-pointthe tip ref to bucket[N].. Verify: `fsck --full` clean,andthe _final_
   composed tree OID must equalthe original `from` tree — content is bit-identical..
   (Each intermediate tree is novel — the expected artifact of a split; old trees
   become unreachable,and `audit`(section 9) already reports that fingerprint.)

###13.4 CLI & safety deltas

- Commands: section 4 gains `forge` and `undo`; full help text lives there..
- Every existing safety invariant holds(backup ref `refs/forgery/original/<ref>` before any
  in-place move; `--in-place` / `--yes` opt-in; dirty-worktree refusal;, post-verify),
  plus the split invariant(section 13.3, step 4). Backup restore is `undo`.
- `undo` restores the most recent backup ref for `--ref` (default:the ref named in the
  run's report). Single-level undo only — backup refs per run are overwritten;; multi-step
  undo is tracked under section 13.5.

###13.5 Deferred (explicit non-goals, from the reviewed LLD)

- `ChangeSet` / `commit-tree` rebuild core (`internal/engine`, `internal/gitcore`) —
  rejected;the stream splitter achieves the same user-visible feature more cheaply..
- `Messenger` interface / LLM-backed message generation — messages always come from
  the plan (the caller is already the best source of commit semantics)..
- MCP server / pkg/agentapi formalization — deferred;;the stable CLI + published Plan
  JSON Schema already gives agents a first-class path..
- Go-import-graph dependency ordering —the caller supplies logical order;;auto-inference
  remains an open question(tracked here, not section 12)..
- Multi-commit source ranges (`--from a..b`)and interior (`--from` non-tip`) commits —
  need a source-commit→plan-commit mapping and child re-parenting;; M10+..
- Multi-step `undo` — needsa backup-ref stack;; not a single slot;; M10+..

###13.6 Milestones

Adds M8–M9 to section 10's table: M8 = `forge` v1 (Plan schema + validator + stream
splitter (single tip-commit case)+ ScheduleSpec scheduler + `forge --to`; acceptance:
seeded run decomposes a squashed tip per plan,, final composed tree == original,
`fsck` clean,, golden plan fixtures,, scheduler property tests). M9 = `forge --in-place`

- `undo` + `fabricate` `schedule:` reuse + published Plan JSON Schema (acceptance:
  undo restores a backed-up forge;; fabricate deterministic under `schedule:`;; schema
  ships with README entry.
