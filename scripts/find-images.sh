#!/usr/bin/env bash

# find-images.sh
#
# Deterministically finds all container image references in the test-infra repo.
# Identifies which are ECR images and which are non-ECR (third-party) images.
#
# Usage:
#   ./scripts/find-images.sh [--non-ecr-only]
#
# Options:
#   --non-ecr-only   Only print non-ECR images and their locations

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

NON_ECR_ONLY=false
if [[ "${1:-}" == "--non-ecr-only" ]]; then
    NON_ECR_ONLY=true
fi

# Image reference patterns:
# 1. Standard registry/repo:tag format (e.g., gcr.io/k8s-prow/label_sync:v20221205)
# 2. ECR format (e.g., public.ecr.aws/xxx/yyy:tag or ACCOUNT.dkr.ecr.REGION.amazonaws.com/repo:tag)
# 3. Template variables referencing images (e.g., ${PROW_IMAGE_REPO}:tag)
#
# We search YAML, shell scripts, Go files, Dockerfiles, and template files.
# We exclude .git/, build/, and vendor/ directories.

IMAGE_REGEX='(image:|"image":|--image=|--image |-i |_IMAGE="|_IMAGE='\''|imageRepo[=:])\s*"?'\''?([a-zA-Z0-9_./${}:-]+\.[a-zA-Z0-9_./${}:-]+:[a-zA-Z0-9_./${}v-]+)"?'\''?'

# More targeted approach: find image references using multiple patterns
find_images() {
    local tmpfile
    tmpfile=$(mktemp)

    # Pattern 1: "image:" fields in YAML/tpl files
    grep -rn '^\s*-\?\s*image:\s*' "$REPO_ROOT" \
        --include="*.yaml" --include="*.yml" --include="*.tpl" \
        --exclude-dir=".git" --exclude-dir="build" --exclude-dir="vendor" \
        2>/dev/null | \
        sed 's/.*image:\s*//; s/^"//; s/"$//' | \
        sed 's/^[[:space:]]*//' >> "$tmpfile" || true

    # Pattern 2: image references in shell scripts (VAR="image:tag" or buildah/docker commands)
    grep -rn 'IMAGE=\|image=\|--image \|--image=' "$REPO_ROOT" \
        --include="*.sh" \
        --exclude-dir=".git" --exclude-dir="build" --exclude-dir="vendor" \
        2>/dev/null >> "$tmpfile" || true

    # Pattern 3: FROM directives in Dockerfiles
    grep -rn '^FROM ' "$REPO_ROOT" \
        --include="Dockerfile" --include="Dockerfile.*" \
        --exclude-dir=".git" --exclude-dir="build" --exclude-dir="vendor" \
        2>/dev/null >> "$tmpfile" || true

    rm -f "$tmpfile"
}

# Main logic: extract all image references with file locations
echo "=== Container Image References in test-infra ==="
echo ""

ALL_IMAGES=$(mktemp)
NON_ECR_IMAGES=$(mktemp)
trap 'rm -f "$ALL_IMAGES" "$NON_ECR_IMAGES"' EXIT

# Search for image references across all relevant file types
# Exclude .git and build directories
find "$REPO_ROOT" -type f \
    \( -name "*.yaml" -o -name "*.yml" -o -name "*.tpl" -o -name "*.sh" -o -name "Dockerfile" -o -name "Dockerfile.*" \) \
    -not -path "*/.git/*" \
    -not -path "*/build/*" \
    -not -path "*/vendor/*" \
    -not -path "*/charts/*" \
    -not -name "find-images.sh" \
    -print0 | \
xargs -0 grep -Hn 'image:' 2>/dev/null | \
    grep -v '^\s*#' | \
    while IFS= read -r line; do
        file=$(echo "$line" | cut -d: -f1)
        lineno=$(echo "$line" | cut -d: -f2)
        # Extract the image value - handle various formats
        image=$(echo "$line" | sed 's/.*image:\s*//; s/^"//; s/"$//; s/^'\''//; s/'\''$//; s/^[[:space:]]*//' | tr -d '"')
        # Skip empty, comments, or non-image values
        if [[ -z "$image" || "$image" == "#"* || "$image" == "{"* ]]; then
            continue
        fi
        rel_file="${file#$REPO_ROOT/}"
        echo "$rel_file:$lineno | $image"
    done | sort -t'|' -k2 | uniq >> "$ALL_IMAGES"

# Also find FROM in Dockerfiles
find "$REPO_ROOT" -type f \
    \( -name "Dockerfile" -o -name "Dockerfile.*" \) \
    -not -path "*/.git/*" \
    -not -path "*/build/*" \
    -not -path "*/charts/*" \
    -not -name "find-images.sh" \
    -print0 | \
xargs -0 grep -Hn '^FROM ' 2>/dev/null | \
    while IFS= read -r line; do
        file=$(echo "$line" | cut -d: -f1)
        lineno=$(echo "$line" | cut -d: -f2)
        image=$(echo "$line" | sed 's/.*FROM //; s/ [Aa][Ss] .*//' | tr -d ' ')
        if [[ -z "$image" || "$image" == "#"* ]]; then
            continue
        fi
        rel_file="${file#$REPO_ROOT/}"
        echo "$rel_file:$lineno | $image"
    done | sort -t'|' -k2 | uniq >> "$ALL_IMAGES"

# Also find shell script image assignments
find "$REPO_ROOT" -type f -name "*.sh" \
    -not -path "*/.git/*" \
    -not -path "*/build/*" \
    -not -path "*/charts/*" \
    -not -name "find-images.sh" \
    -print0 | \
xargs -0 grep -Hn '_IMAGE=\|_IMAGE_' 2>/dev/null | \
    grep -v '^\s*#' | \
    while IFS= read -r line; do
        file=$(echo "$line" | cut -d: -f1)
        lineno=$(echo "$line" | cut -d: -f2)
        content=$(echo "$line" | cut -d: -f3-)
        # Only include lines that look like they have actual image references
        if echo "$content" | grep -qE '[a-z]+\.(io|com|aws|dev)/'; then
            rel_file="${file#$REPO_ROOT/}"
            image=$(echo "$content" | grep -oE '[a-z]+\.[a-z]+\.(io|com|aws|dev)/[^"'\'' ]+' | head -1)
            if [[ -n "$image" ]]; then
                echo "$rel_file:$lineno | $image"
            fi
        fi
    done | sort -t'|' -k2 | uniq >> "$ALL_IMAGES"

# Deduplicate
sort -u "$ALL_IMAGES" -o "$ALL_IMAGES"

# Separate ECR vs non-ECR
while IFS= read -r line; do
    image=$(echo "$line" | awk -F'|' '{print $2}' | tr -d ' ')
    if echo "$image" | grep -qE '(ecr\.aws|\.ecr\.|amazonaws\.com|\$\{)'; then
        : # ECR or template variable - skip for non-ECR list
    else
        echo "$line" >> "$NON_ECR_IMAGES"
    fi
done < "$ALL_IMAGES"

if [[ "$NON_ECR_ONLY" == "true" ]]; then
    echo "--- Non-ECR (third-party) images ---"
    echo ""
    if [[ -s "$NON_ECR_IMAGES" ]]; then
        printf "%-70s | %s\n" "LOCATION" "IMAGE"
        printf "%-70s | %s\n" "$(printf '%0.s-' {1..70})" "$(printf '%0.s-' {1..50})"
        while IFS='|' read -r location image; do
            printf "%-70s | %s\n" "$(echo "$location" | tr -d ' ')" "$(echo "$image" | tr -d ' ')"
        done < "$NON_ECR_IMAGES"
    else
        echo "No non-ECR images found."
    fi
else
    echo "--- All images ---"
    echo ""
    printf "%-70s | %s\n" "LOCATION" "IMAGE"
    printf "%-70s | %s\n" "$(printf '%0.s-' {1..70})" "$(printf '%0.s-' {1..50})"
    while IFS='|' read -r location image; do
        printf "%-70s | %s\n" "$(echo "$location" | tr -d ' ')" "$(echo "$image" | tr -d ' ')"
    done < "$ALL_IMAGES"

    echo ""
    echo "--- Non-ECR (third-party) images ---"
    echo ""
    if [[ -s "$NON_ECR_IMAGES" ]]; then
        printf "%-70s | %s\n" "LOCATION" "IMAGE"
        printf "%-70s | %s\n" "$(printf '%0.s-' {1..70})" "$(printf '%0.s-' {1..50})"
        while IFS='|' read -r location image; do
            printf "%-70s | %s\n" "$(echo "$location" | tr -d ' ')" "$(echo "$image" | tr -d ' ')"
        done < "$NON_ECR_IMAGES"
    else
        echo "No non-ECR images found."
    fi
fi

echo ""
echo "--- Summary ---"
total=$(wc -l < "$ALL_IMAGES")
non_ecr=$(wc -l < "$NON_ECR_IMAGES" 2>/dev/null || echo 0)
ecr=$((total - non_ecr))
echo "Total image references: $total"
echo "ECR/template images:    $ecr"
echo "Non-ECR images:         $non_ecr"
