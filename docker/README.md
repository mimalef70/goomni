# GoOmni

One self-hosted Go gateway for independent **Bale, Eitaa and Rubika** accounts.
Use a shared REST API, durable send queues, scheduling and signed webhooks from
your backend. The English/Persian account and webhook panel is embedded in the
binary. Sponsored by [MuChat](https://mu.chat); independent MIT open-source software.

Version 2.4.1 corrects Eitaa/Rubika recovery boundaries and sender-name enrichment,
isolates unavailable Rubika peers and fixes missing-size avatar downloads. It
preserves storage schema 9, the shared SQLite database and accepted work.

- [Source and quickstart](https://github.com/mimalef70/goomni)
- [Release 2.4.1 and checksums](https://github.com/mimalef70/goomni/releases/tag/v2.4.1)
- [API reference](https://mimalef70.github.io/goomni/)
- [Consumer integration](https://github.com/mimalef70/goomni/blob/v2.4.1/docs/consumer-integration.md)
- [Upgrade from GoBale](https://github.com/mimalef70/goomni/blob/v2.4.1/docs/upgrade-goomni.md)

## Images

```sh
docker pull mimalef70/goomni:v2.4.1
```

The versioned image supports **Linux amd64 and arm64** and is also published as
`ghcr.io/mimalef70/goomni:v2.4.1`. Both registries contain the same immutable build.
Use an explicit version tag or the digest from the release notes. The runtime is
one non-root Go process with SQLite; Python, Node, a browser, Redis and PostgreSQL
are not runtime requirements. License notices are included in the image.

## New installation

Download [docker-compose.yml](https://raw.githubusercontent.com/mimalef70/goomni/v2.4.1/docker-compose.yml)
into a private installation directory, then run:

```sh
export APP_IMAGE='docker.io/mimalef70/goomni:v2.4.1'
docker pull "$APP_IMAGE"
docker run --rm --user "$(id -u):$(id -g)" \
  --volume "$PWD:/config" --workdir /config \
  "$APP_IMAGE" init --bale-web-client
export APP_MASTER_KEY="$(cat master.key)"
docker compose up -d --no-build
docker compose logs --tail=100 goomni
```

Open `http://127.0.0.1:3000/ui/` and use the generated `APP_BASIC_AUTH`. Connect an
account using its own phone/code/password flow. The credential is gateway-wide
administration: keep it in your backend, never in an end user's browser.

Bale is enabled by default. Add `EITAA_ENABLED=true` and/or
`RUBIKA_ENABLED=true` to the local `.env` to opt in, then recreate the container.
Read `GET /app/providers` and the selected account's capability response before
offering a feature. Public protocol changes can affect any unofficial adapter.

## Storage and upgrades

**Existing GoOmni or GoBale installation? Follow the upgrade guide before starting.** Stop
the old service and make a full manual snapshot. Keep its original database,
media, encryption key and actual Docker volume. Set `APP_DATA_VOLUME` to that
volume and `APP_DATABASE` to its original container path, often
`/app/storages/gobale.db`. Do not run `init` again. Never use
`docker compose down -v` to upgrade.

GoOmni 2.4.1 upgrades schema 8 to 9 transactionally on startup, backfilling local
conversation summaries. Allow time and temporary disk space for a large event
journal. Rolling back to a binary requiring an older storage schema needs its
matching pre-upgrade snapshot.

New installations use `goomni-data` and `/app/storages/goomni.db`. Run exactly one
process per database/media root. Sessions and stored secrets are encrypted;
ordinary event bodies and media require filesystem/volume protection. History
and registered media have no automatic expiry. Back up the full storage and
matching master key, and monitor disk usage.

## Verified scope

Controlled two-account tests cover native login, restart, selected messaging,
media, group/channel operations and signed webhooks. A one-hour Eitaa/Rubika
process outage recovered all controlled test messages. This does not establish
months-old archives, multi-day recovery, every operation or 300 live accounts.
Eitaa/Rubika remain opt-in; Eitaa poll/typing and some media presentation variants
retain documented limits. Unknown sends are not automatically retried.

Read the [dated acceptance record](https://github.com/mimalef70/goomni/blob/v2.4.1/docs/providers/acceptance.md)
for exact builds, failures and remaining gates. Report bugs in
[GitHub issues](https://github.com/mimalef70/goomni/issues) without phone numbers,
OTPs, tokens, sessions, databases or private messages.
