# Deployment agent rules

> **This is a template, and it is a floor.** It is not authoritative for any
> community.
>
> It governs agents operating a *deployed* paisans stack. It does not govern
> agents working on the Paisans toolkit itself, which has its own
> `AGENTS.md` at the root of that repository.
>
> **Copy it into your deployment's own configuration repository as `AGENTS.md`**
> and edit it to fit. Under that name, every agent that opens that repository
> reads it without being told to.
>
> **A community's Charter still governs.** The Charter is that community's
> mission, values and code of conduct, and it lives in the community's wiki, not
> here. This file is a rulebook derived from those values, not a substitute for
> them. Where a rule here conflicts with the community's Charter, or with an
> explicit instruction from the community's admins, the Charter wins and the
> rule here is the one that is wrong.
>
> **The second situation it exists for** is a community that has lost its wiki
> along with everything else, where whoever is rebuilding it (person or agent)
> needs to know what may be done and by whom before the wiki comes back. That is
> why the rules here are written to stand alone. The mission, the values, the
> code of conduct, the split protocol and every choice specific to a community
> are absent by design, because they are not generic and because publishing them
> here would put a deployment's shape in a third party's repository.
>
> **After restoring from backup, reconcile against the real thing.** See *After
> a restore* at the end.

## What this covers

Three things, chosen because they are the ones that transfer between communities
without alteration:

* **Decision authority**: what an agent may settle alone, and what belongs to a
  human.
* **Blast radius**: how dangerous an action is, and how much human involvement
  it therefore requires.
* **Services the deployment runs**: how software the deployment runs may make
  a change the blast radius tiers gate for an agent.

Everything a community decides for itself is out of scope here, starting with
its values. Those live in its Charter.

## Decision authority

**An agent decides alone:** container names, flag values, file layout, document
structure, which runbook to write, how to word machine-generated sysadmin
documentation.

**A human decides:** which software fills a capability; anything changing the
identity model; federation policy; anything that locks in a migration path; when
a service is exposed to the public internet.

An agent choosing between two candidate applications has left its lane. Research
and recommend; the human chooses.

## Blast radius

When an action matches more than one tier, **the strictest applicable tier
governs**. An agent unsure which tier an action falls under must treat it as the
stricter one.

**Free — do it and record it**

* Reading host state
* Writing documentation
* Editing a repository on a branch
* Restarting a service that is already sanctioned and running

**Requires explicit human approval, every time**

* Stopping or removing containers
* Deleting volumes or images
* Reverse-proxy changes touching live routes
* Bringing up a new container or stack on a shared host — a first deployment,
  not a restart of something already sanctioned
* Any mutation of the identity provider's clients, groups or users, except
  the ones below
* Deleting documentation

The exception: **`paisans apply` creating the OIDC client an app declared in
`paisans-stack.yaml` needs**, as part of setting that app up, takes no approval of its
own. Every app in the stack signs in through the identity provider, so an app
cannot run without its client, and approval to run the apply is approval for the
clients it creates, whether a person or an agent runs it. The gate, which no
file declares but the toolkit runs whenever an app sets `gated` or
`auto_login`, is covered the same way: setting either key is the approval for
the gate's client. It holds whoever wrote the app's blueprint: an app whose
blueprint comes from a third party catalog is approved by being declared, the
same as one from the toolkit's own. The exception covers
creating a missing client, and restricting it to the app's group, only. Rotating
a client's secret, deleting a client, and every other change to groups or
users stay in the tier above, and `apply` never replaces a client it has
already recorded.

The same holds for the deployment's own operators. **`paisans apply`
making each operator `paisans-stack.yaml` declares an account at the identity
provider**, and handing each one who has no passkey yet a one-time login
link valid for a day, takes no approval of its own: a deployment nobody can
sign into is not set up, and declaring the operator is approval for the account.
The exception covers creating that account, marking its declared email
verified, adding it to the group `operators`, the member group `members` and
every app's admin and member groups, issuing the link and having the identity
provider email it. It covers making the account an administrator of the
identity provider only where the identity provider's app declares
`management: pocket-id`; under `management: paisans` `apply` makes nobody one.
Doing any of these by hand, and every other change to a user, stays in the
tier above.

**`paisans apply` creating a missing group** takes no approval of its own
either, but only a group the deployment's configuration names: `operators`,
the member group, each app's admin and member groups, and each group the
`pocket-id` app's `signup_default_groups` lists. Those groups are what the
operators, new members and the apps' clients are put in and restricted to, so
an apply that could not create them could not set the deployment up, and
declaring them is approval for them. Creating any other group, and every
change to a group that exists, stays in the tier above.

**Never without the human present**

* Anything touching member data
* Exposing a service to the public internet
* Exposing a service that has no prior live route
* Changing a federation allowlist
* Changing private-network access control
* Deleting backups
* Removing or rotating the identity provider's API key

The distinction between the last two tiers matters and is easy to blur.
*Approved* means a human said yes to this specific action. *Present* means a
human is there while it happens, because the action cannot be undone and its
consequences reach people who are not in the room.

## Services the deployment runs

The tiers above govern actions: what an agent, or a person at a keyboard,
does to a running deployment. Some changes the tiers gate can instead be made by
a service the deployment runs, on its own schedule, with no agent involved.
That is a different context, and the tiers do not apply to it write by write.
The operator reconciler makes no such change: it reads the identity provider
and writes nothing. The account service does, and is approved under this
section (see *The account service* below).

**A service is approved once, as a whole.** A human approves its design, and
it is deployed. What it may change is then fixed in its code rather than
decided at run time. That approval stands only while:

* every kind of write it makes is named in its design and in a decision record
* its tests fail if it makes any other write
* it logs every write it makes, so that what it did can be audited afterwards

**The approval covers the service, not the change.** So:

* Deploying a new such service, or adding or widening a write in one that
  exists, is a decision for a human, in the tier of the strictest write it
  would make. Approving the design is that approval.
* Running the service as approved (restarting it, or redeploying it
  unchanged) is in the tier of restarting any sanctioned service.
* An agent gains no authority from the service. It may not make the service's
  change by hand, use the service's credential to make it, or change what the
  service reads in order to steer what it writes. Each of those is an agent's
  action, in the tier of the change itself.

### The account service

The account service writes to the identity provider with the identity
provider's static API key, and makes these writes and no others:

* **Signup:** creating a user when a signup is accepted. It sends no group;
  the identity provider puts the new user in the member group, its signup
  default group, as `apply` configured it.
* **Bans:** disabling a user, and re-enabling them when the ban is lifted.
* **A member's address and name:** setting a user's email address, marked
  verified, after an address change or a recovery, and setting a user's
  display, first and last names.
* **Sign-in links:** asking the identity provider to email a user a one-time
  access link, after a signup or a recovery.
* **Groups:** replacing a user's groups (`PUT /api/users/:id/user-groups`)
  with the list it has just read, plus the groups an admin added and minus
  those the admin removed. It never changes membership of `operators`, which
  only `paisans-stack.yaml` decides (the toolkit's
  `docs/specs/2026-10-14-account-promotion.md`).

It meets the three conditions:

* Every write is named in its specs, in the toolkit's `docs/specs/`
  (`2026-10-14-account-service.md`, `2026-10-14-account-signup-bans-recovery.md`
  and `2026-10-14-account-promotion.md`), and in the toolkit's
  `docs/decisions.md`.
* Its write-surface test fails on any other write to the identity provider:
  another route, another method, or another field in a request body.
* Every admin action is an entry in its audit log, and every write to the
  identity provider is logged.

Any write beyond these is a widening, and a decision for a human as above.
Changing a user's groups by hand, or with the service's key, stays in the
tier above, as every other change to a user does.

## If you are an agent reading this because the wiki is unreachable

You may act on this document, and you should say so before you do: *"the wiki is
unreachable; operating from the agent rules template."* Never let that pass
silently.

Four things bound it:

* **Read every tier as the stricter option.** This is a floor. The community's
  own rules may be stricter and you cannot check.
* **You cannot know you are alone.** Session coordination normally works through
  a claim written into the community's wiki. Without it, another session may be
  working the same area and neither of you can tell. Ask the human to confirm no
  other session is active before touching anything shared, and prefer work that
  cannot collide.
* **This document does not contain the community's particulars.** Which software
  fills a capability, federation policy, the identity model: all absent here,
  and all still a human's decision.
* **Record everything you do**, including anything done without the approval that
  would normally have been required, and why. It has to be reconciled once the
  wiki is back.

You know what tier an action falls in. You do not know this community's specifics
and you do not know who else is working. Act accordingly.

## During a recovery, specifically

A catastrophic recovery is unusual in one helpful way: **a human is definitionally
involved**, because someone is rebuilding a machine. That does not lower any tier.
It means the human-present requirement is already satisfied for the steps that
need it, and the approval requirement should be met by asking rather than
inferring from the emergency.

Emergencies are exactly when "they would obviously have said yes" feels most
reasonable and is most often wrong. If the community's own rules are
unreachable, assume they are **stricter** than this document, not more
permissive.

## After a restore

Once the wiki is back:

1. **Read the community's own `AGENTS.md` and its Charter.** Its own rules
   replace this template, and its Charter governs both.
2. **Re-check every assumption made during the recovery** against them. Anything
   done under this template that the community's own rules would have gated
   differently should be recorded, and raised with the community's admins.
3. **Record what was done while the wiki was unavailable**, including anything
   done without the approval that would normally have been required, and why.
4. **Reconcile this template with what the recovery taught you.** If the
   community's own rules differ, theirs are right. If the difference is
   something any community would want, it belongs upstream in the toolkit
   repository as well.

## For an operator adopting this stack

Copy this file into your deployment's configuration repository as `AGENTS.md`
and edit it to fit. It is deliberately generic, and a real community will want
to be more specific (Eg: who counts as "a human", which services are sanctioned,
which hosts an agent may reach, and anything its own history has taught it).

Then write your Charter, which is the part this template does not contain and
cannot: the mission, the values and the code of conduct that these rules exist
to serve.
