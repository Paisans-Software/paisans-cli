# No-mail DNS records, and names with mail of their own

Date: 2026-10-11. Status: approved by the founder in session.

A name that publishes an address can be forged as the sender of a message
whether or not anybody sends mail from it. `dns init` already publishes every
name a deployment answers on, so it now also publishes, beside each one, the
records that tell a receiver the name sends and receives no mail, except at a
name whose mail somebody else runs.

## Scope

In: `community.mail_domains` in `paisans.yaml`, the records `dns.Desired`
derives, how `dns init` compares TXT and MX records with what the provider
holds, which records `dns prune` may delete, the Cloudflare provider's MX
priority, two `validate` rules, the example and the README.

Out:

* **Writing any mail service's records.** A name with mail gets nothing from
  the toolkit: its SPF, MX, DKIM keys and DMARC policy belong to whoever runs
  its mail.
* **`site remove`'s DNS stage.** It still deletes address records only. A
  no-mail record left at a removed site's endpoint name is `dns prune`'s.
* **Updating a record.** As before, nothing in the toolkit updates one.

## `paisans.yaml`

```yaml
community:
  domain: example.org
  mail_domains: [example.org]   # optional
```

The names with mail are `community.mail_domains` together with the domain of
`smtp.from_address` and of the `from_address` in the `smtp` block of every app
whose kind sends mail (`kinds.SendsMail`). The second set is derived, so the
toolkit never locks out mail it configured itself. Names are compared
lowercased, without a trailing dot.

`validate` refuses an entry that is not a hostname
(`mail-domain-is-not-a-hostname`) or is neither `community.domain` nor under
it (`mail-domain-outside-the-domain`).

## Records

For every name that gets an address record, and for `community.domain`, unless
the name has mail: TXT `v=spf1 -all`.

For `community.domain`, unless it has mail:

| Name | Record |
|------|--------|
| `community.domain` | MX, preference 0, target `.` |
| `*._domainkey.<domain>` | TXT `v=DKIM1; p=` |
| `_dmarc.<domain>` | TXT `v=DMARC1; p=reject; sp=reject; adkim=s; aspf=s;` |

When `community.domain` has mail, `_dmarc.<domain>` is an **advisory** want:
checked and never created, and never counted as wanted by prune.

Every record carries the comment `dns init` already writes. Cloudflare's MX
create sends `priority: 0` explicitly.

The zone walk does not ask whether a name beginning with a `*` or `_` label is
a zone, since such a name is always a record inside one.

## Comparing with the provider

Only a record of the same kind competes with a wanted one; any other TXT at
the name is ignored. The kinds:

| Wanted | Competes with | Present when | Otherwise |
|--------|---------------|--------------|-----------|
| SPF | TXT whose text starts `v=spf1` | exactly `v=spf1 -all`, whitespace collapsed, and the only one | conflict |
| null MX | any MX | preference 0, target `.` | conflict |
| empty DKIM key | any TXT at the name | tag `p` present and empty | conflict |
| DMARC | TXT whose text starts `v=DMARC1` | `p=reject`, `sp` (or `p` without it) `reject`, `pct` absent or 100 | conflict, or for an advisory want a warning |

TXT text is read as a receiver reads it: quoted strings joined with nothing
between. A CNAME at the name is a conflict, as for address records. A missing
advisory policy is a warning. Conflicts on SPF and MX say to add the name to
`community.mail_domains` if it has mail.

## Prune

Rule 2 widens: an A or AAAA record, or a TXT or MX record whose content is
exactly one of the four no-mail contents at the kind of name it is written at
(`_dmarc.` names for DMARC, `*._domainkey.` names for DKIM, any name for SPF
and the null MX). For such a record rule 4, the address rule, does not apply.
Rules 1, 3 and 5 are unchanged, with advisory wants not counted as wanted.

So listing a name in `community.mail_domains` and running `dns prune
--execute` removes the toolkit's no-mail records there, which must happen
before the mail service's own SPF and MX are added.

## Amendment, 2026-10-11: a leftover stops dns init

Founder decision, the same day. `dns init`, and so `apply`, refuses while a
no-mail record this deployment made sits at a name that now has mail, so that
the order in *Prune* cannot be skipped silently.

* For each mail name equal to or under `community.domain`, `Desired` adds
  **leftover** wants: the SPF content at the name, and at the domain also the
  null MX and the empty DKIM key. A mail name outside the domain is never
  read, since `dns init` never wrote there and its zone may not be visible.
* `Build` looks at the records at a leftover want's name, and at the advisory
  DMARC want's, for one of the same kind that carries this deployment's
  comment and is exactly the no-mail content (`isLockdown`, the test prune
  uses). One found is a conflict whose detail says to run `paisans dns prune
  --execute`; none is `clear`, which is not shown. A leftover is never
  created and never counted as wanted by prune.
* The plan shows such a conflict with `remove with dns prune:` in place of
  `wanted:`, and `apply`'s stop hint says to resolve each conflict as it says.


`internal/dns/mail_test.go`: the records derived with no mail, with a mail
domain, and with smtp senders; every row of the comparison table; creation and
read-back including the MX priority; the advisory warning writing nothing;
prune removing exactly the no-mail records a mail domain stops wanting; site
removal leaving them; each leftover a conflict naming prune, none from a
record without this deployment's comment, none after prune has run, and no
lookup of a mail name outside the domain. `internal/validate`: one fixture
per new rule.
