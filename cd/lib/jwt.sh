#!/usr/bin/env bash

#
# JWT Encoder Bash Script; adapted from https://www.willhaley.com/blog/generate-jwt-with-bash/
#

usage() { echo "Usage: $0 -k </path/to/github/private/key> -i <github_app_id>" 1>&2; exit 1; }

JWT_SIGNING_ALGO="RS256"

while getopts ":k:i:" arg; do
  case "${arg}" in
    k)
      GITHUB_PRIVATE_KEY_PATH=${OPTARG}
      ;;
    i)
      GITHUB_APP_ID=${OPTARG}
      ;;
    *)
      usage
      ;;
  esac
done
shift $((OPTIND-1))

if [ -z "${GITHUB_APP_ID}" ] || [ -z "${GITHUB_PRIVATE_KEY_PATH}" ]; then
  usage
fi

now_sec=$(date +"%s")
iat_sec=$((now_sec - 60)) # issued at - one minute ago to account for clock drift
exp_sec=$((now_sec + 600)) # expires - 10 min max

header="{
	\"alg\": \"$JWT_SIGNING_ALGO\"
}"

payload="{
  \"iat\": $iat_sec,
  \"exp\": $exp_sec,
	\"iss\": \"$GITHUB_APP_ID\"
}"

base64_encode()
{
	local input=${1:-$(</dev/stdin)}
	# Use `tr` to URL encode the output from base64.
	echo "${input}" | base64 | tr -d '=' | tr '/+' '_-' | tr -d '\n'
}

json() {
	local input=${1:-$(</dev/stdin)}
	echo "${input}" | jq -c .
}

hmacsha256_sign()
{
	local input=${1:-$(</dev/stdin)}
	echo "${input}" | openssl dgst -binary -sha256 -sign "${GITHUB_PRIVATE_KEY_PATH}"
}

header_base64=$(echo "${header}" | json | base64_encode)
payload_base64=$(echo "${payload}" | json | base64_encode)

header_payload=$(echo "${header_base64}.${payload_base64}")
signature=$(echo "${header_payload}" | hmacsha256_sign | base64_encode)

echo "${header_payload}.${signature}"