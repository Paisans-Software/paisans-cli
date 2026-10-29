#!/bin/sh
# Rendered by paisans. Do not edit: `paisans deployment apply` overwrites this file.
#
# Converges Beszel's sign in on stats through the PocketBase v0.40.5
# API, at every start of this container, so the hub holds what
# paisans-stack.yaml and the secrets file say whatever was changed in its UI:
#
#   * the users collection's OAuth2 settings, one provider, oidc, at the
#     deployment's Pocket ID with this app's client;
#   * the break glass user's password, which USER_PASSWORD sets on a new
#     install only. The user and the superuser are separate accounts
#     (beszel-docs user-accounts.md:3-5), and admin sets the superuser's.
#
# It signs in as the superuser admin has just set, so it carries
# admin_password and the client secret, and is 0600. Every request body is a
# quoted here-document, so no value in it passes through the shell. The image
# has curl and a shell but no jq, so the two values read back are cut out with
# sed. Success leaves the container running asleep, so `restart: on-failure`
# restarts it only when a step failed.
set -eu

hub=http://app:8090

# PocketBase's health route answers 200 with no authentication
# (apis/health.go:11-25). The hub is healthy before admin runs and admin
# before this, so the wait covers only a hub restarted in between.
tries=0
until [ "$(curl -s -o /dev/null -w '%{http_code}' "$hub/api/health")" = 200 ]; do
	tries=$((tries + 1))
	if [ "$tries" -ge 60 ]; then
		echo "setup: the hub at $hub did not answer /api/health with 200 in two minutes" >&2
		exit 1
	fi
	sleep 2
done

# A superuser signs in at the _superusers auth collection with identity and
# password (apis/record_auth_with_password.go:102-105), and the reply's token
# goes in the Authorization header as it is.
auth=$(curl -sS --fail-with-body -X POST "$hub/api/collections/_superusers/auth-with-password" \
	-H 'Content-Type: application/json' --data-binary @- <<'JSON'
{"identity":"beszel-admin@example.org","password":"fixture-not-a-secret-stats-admin"}
JSON
)
token=$(printf '%s' "$auth" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
if [ -z "$token" ]; then
	echo "setup: signing in as the superuser answered without a token" >&2
	exit 1
fi

# The oauth2 field of an auth collection, and each provider's fields, are
# core/collection_model_auth_options.go:126, :407-413 and :527-543. Beszel's
# UI offers a provider whose name starts with oidc as its OpenID Connect
# button (internal/site/src/components/login/auth-form.tsx:50-51), and
# PocketBase's oidc provider is that name (tools/auth/oidc.go:27). The list
# written is the whole list, so a provider added in the UI is removed.
curl -sS --fail-with-body -o /dev/null -X PATCH "$hub/api/collections/users" \
	-H "Authorization: $token" -H 'Content-Type: application/json' --data-binary @- <<'JSON'
{"oauth2":{"enabled":true,"providers":[{"name":"oidc","displayName":"Pocket ID","clientId":"fixture-not-a-secret-stats-id","clientSecret":"fixture-not-a-secret-stats-secret","authURL":"https://id.example.org/authorize","tokenURL":"https://id.example.org/api/oidc/token","userInfoURL":"https://id.example.org/api/oidc/userinfo","pkce":true}]}}
JSON

# The break glass user, by its email, which is unique in an auth collection
# (core/collection_model.go:1036-1060). A superuser sets a password without
# the old one (forms/record_upsert.go:184-193). The address is
# beszel-admin@ under community.domain, which holds only letters, digits,
# hyphens and dots, so it is safe in the filter below. An operator who has
# deleted the user keeps it deleted.
#
# Setting the password on every start, even to the value it already has,
# stores a new bcrypt hash, and PocketBase gives an auth record a new token
# key whenever its stored password changes (core/record_model.go,
# onRecordSaveExecute), which signs out every break glass session. That is
# intended: break glass is for while the identity provider is down, and a
# session of it does not outlive the next apply or restart of this stack.
users=$(curl -sS --fail-with-body -G "$hub/api/collections/users/records" \
	-H "Authorization: $token" --data-urlencode "filter=email='beszel-admin@example.org'" --data-urlencode 'fields=id')
id=$(printf '%s' "$users" | sed -n 's/.*"items":\[{"id":"\([^"]*\)".*/\1/p')
if [ -n "$id" ]; then
	curl -sS --fail-with-body -o /dev/null -X PATCH "$hub/api/collections/users/records/$id" \
		-H "Authorization: $token" -H 'Content-Type: application/json' --data-binary @- <<'JSON'
{"password":"fixture-not-a-secret-stats-admin","passwordConfirm":"fixture-not-a-secret-stats-admin"}
JSON
else
	echo "setup: no user beszel-admin@example.org to set the password of; the superuser's is set"
fi

echo "setup: the hub's sign in is written"
trap 'exit 0' TERM INT
while :; do
	sleep 86400 &
	wait $!
done
