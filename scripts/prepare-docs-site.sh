#!/usr/bin/env bash
# Prepares the input of the MkDocs site in .site-src/ (not committed): a copy of docs/ with the README as the
# home page, and with the links that would not resolve on the site pointed at the repository.
# docs/ itself is not touched.
set -euo pipefail
cd "$(dirname "$0")/.."

repo=${GITHUB_REPOSITORY:-sasano8/pinner}
base="https://github.com/${repo}/blob/main"

rm -rf .site-src
cp -r docs .site-src

# The README sits outside docs/ and links to files outside it.
sed -e "s#(\./docs/#(./#g" \
    -e "s#(\./test/#(${base}/test/#g" \
    -e "s#(\./\.github/#(${base}/.github/#g" \
    -e "s#(\./IDEA\.md)#(${base}/IDEA.md)#g" \
    README.md > .site-src/index.md

# The design notes and the research link to the README and IDEA.md, which are not in docs/.
for f in .site-src/design.md .site-src/research.md; do
  sed -i -e "s#(\.\./README\.md)#(index.md)#g" \
         -e "s#(\.\./IDEA\.md)#(${base}/IDEA.md)#g" "$f"
done
