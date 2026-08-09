# Security Policy

## Reporting a vulnerability

**Please do not open a public issue.** Use GitHub's private reporting:
[Report a vulnerability](../../security/advisories/new). If that is unavailable
to you, open a public issue containing only "requesting a security contact" and
no detail, and you will be pointed somewhere private.

This is a solo-maintained project. Expect an acknowledgement within a week, and
please assume good faith about the timeline rather than a policy of silence.
You will be credited in the advisory unless you would rather not be.

## What NetInv holds that is worth attacking

Worth stating plainly, because it shapes what counts as a serious report:

- **SNMP credentials for your network devices**, including SNMPv3 auth and
  privacy passphrases. They are encrypted at rest with a master key
  (`NETINV_MASTER_KEY`) held outside the database. Anyone who obtains both the
  database and that key obtains write-capable credentials to your network
  equipment. There is a `TestNoSecretLeakInvariant` regression test asserting
  plaintext never lands in a column.
- **A complete map of your network** — device inventory, addresses, interface
  topology, LLDP adjacency, traffic volumes. Useful reconnaissance even without
  the credentials.
- **User accounts and session tokens** for the portal itself.

Reports touching credential handling, authentication, authorization scoping
between sites, or the metrics proxy are the ones most likely to matter.

## Deployment posture you should know about

NetInv is in **pilot** status. Some hardening is deliberately not done yet, and
these are documented limitations rather than vulnerabilities:

- **The quickstart deployment serves plain HTTP** on port 8090 with
  `NETINV_INSECURE_COOKIES=1`. It is meant for an evaluation host on a trusted
  network. Do not put it on the internet without a TLS-terminating reverse
  proxy in front, `NETINV_UI_URL` set to the HTTPS URL, and
  `NETINV_INSECURE_COOKIES` removed.
- **The security checklist in [doc 20](docs/20-security-design.md) §12 has not
  yet been run end-to-end against a TLS deployment.** It is one of the
  outstanding gates for v1.0.
- **Multi-tenancy is designed-in but dormant.** Do not rely on it for isolation
  between untrusted parties; today's model is a single organization.
- **Back up `NETINV_MASTER_KEY` separately from the database.** Losing it means
  re-entering every SNMP credential (risk R-10 in
  [doc 28](docs/28-risk-assessment.md)). Storing it *with* the database backup
  defeats the encryption entirely.

## Out of scope

- Findings that require an attacker who already has the master key, database
  superuser, or host root — those are game over by construction, not bugs.
- Missing hardening on the bundled evaluation containers (MailHog, the SNMP
  simulator, RabbitMQ's management UI). They exist to make the quickstart work
  and are not intended for production; see
  [doc 32](docs/32-quickstart.md) for how to point NetInv at your own data tier.
- Denial of service by pointing NetInv at devices that answer slowly or
  enormously. Polling behaviour is tunable, and reports of *silent data loss*
  under those conditions are very much in scope — but a slow poll is not.

## Supported versions

Pre-1.0, only `main` is supported. There are no backported fixes yet.
