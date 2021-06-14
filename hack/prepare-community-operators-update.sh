#!/usr/bin/env bash

set -o nounset
set -o pipefail
set -o errexit
set -x

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
__root="$(cd "$(dirname "${__dir}")" && pwd)"

# Required values
if [[ -z "${GITHUB_USER}" ]]; then
	echo "Must specify GITHUB_USER" 1>&2
	exit 1
fi

if [[ -z "${GITHUB_TOKEN}" ]]; then
	echo "Must specify GITHUB_TOKEN" 1>&2
	exit 1
fi

if [[ -z "${CHANNEL}" ]]; then
	echo "Must specify CHANNEL" 1>&2
	exit 1
fi

if [[ -z "${OPERATOR_VERSION}" ]]; then
	echo "Must specify OPERATOR_VERSION" 1>&2
	exit 1
fi
# ie. assisted-service-operator
OPERATOR_NAME="${OPERATOR_VERSION%.v*}"
# ie. 0.0.5-rc.2
OPERATOR_PACKAGE_VERSION="${OPERATOR_VERSION#*.v}"

CSV="${OPERATOR_NAME}.clusterserviceversion.yaml"
MANIFESTS_DIR="${__root}/${BUNDLE_OUTPUT_DIR}/manifests"
BASE_CSV="${MANIFESTS_DIR}/${CSV}"
COMMUNITY_OPERATORS_GIT="https://github.com/operator-framework/community-operators.git"
COMMUNITY_OPERATORS_FORK="https://${GITHUB_USER}:${GITHUB_TOKEN}@github.com/${GITHUB_USER}/community-operators.git"
COMMUNITY_OPERATORS_PR_TEMPLATE="https://raw.githubusercontent.com/operator-framework/community-operators/master/docs/pull_request_template.md"
OPERATOR_DIR="community-operators/${OPERATOR_NAME}/"
OPERATOR_VERSION_DIR="${OPERATOR_DIR}/${OPERATOR_PACKAGE_VERSION}"
OPERATOR_CSV="${OPERATOR_VERSION_DIR}/${CSV}"
OPERATOR_PACKAGE="${OPERATOR_DIR}/assisted-service.package.yaml"


TMP_DIR=$(mktemp -d)
pushd ${TMP_DIR}
git clone "${COMMUNITY_OPERATORS_FORK}"
cd community-operators
git remote add upstream ${COMMUNITY_OPERATORS_GIT}
# Need to inject "upstream" manually into .git/config so that
# we can specify it as the base for `gh` commands.
# cat <<EOF >> .git/config
# [remote "upstream"]
# 	url = ${COMMUNITY_OPERATORS_GIT}
# 	fetch = +refs/heads/*:refs/remotes/upstream/*
# 	gh-resolved = base
# EOF
git fetch upstream master:upstream/master
git checkout upstream/master
git checkout -b ${OPERATOR_PACKAGE_VERSION}

# Fix version
UNRELEASED_VERSION=$(grep -e 'version:.*unreleased' ${BASE_CSV} | awk '{print $2}')
cp -r ${__root}/${BUNDLE_OUTPUT_DIR}/manifests community-operators/${OPERATOR_NAME}/${OPERATOR_PACKAGE_VERSION}
sed -i "s/${UNRELEASED_VERSION}/${OPERATOR_PACKAGE_VERSION}/" ${OPERATOR_CSV}

# Update package
PREV_OPERATOR_VERSION=$(grep -B 1 ${CHANNEL} ${OPERATOR_PACKAGE} | head -n 1 | awk '{print $3}')
sed -i "s/${PREV_OPERATOR_VERSION}/${OPERATOR_VERSION}/" ${OPERATOR_PACKAGE}

# Commit
git add --all
git commit -s -m "assisted-service-operator: Update to ${OPERATOR_VERSION#*.v}"
git push --set-upstream --force origin HEAD

# Create PR
gh pr create \
	--draft \
	--base upstream:master \
	--title "$(git log -1 --format=%s)" \
	--body "$(curl -sSLo - ${COMMUNITY_OPERATORS_PR_TEMPLATE} | \
		sed -r -n '/#+ Updates to existing Operators/,$p' | \
		sed -r -e 's#\[\ \]#[x]#g')"

popd
rm -rf ${TMP_DIR}
