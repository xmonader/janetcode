# Isolated patch publishing experiment

JanetCode edits and tests in a disposable Docker container. The container
receives a read-only copy of the PR commit, a temporary checkout, and settings
for one model provider. The trusted publisher owns the local bare repository.

## Run the self-contained experiment

With Docker and Git on Linux, run:

```sh
docker pull golang:1.26.10
ci/patch-publish/test.sh
```

The test uses a scripted editor and a temporary Git repository. It checks that
the editor cannot see the publisher's repository or GitHub credentials, that
the approved patch lands byte-for-byte, and that changed patches, stale
approvals, and concurrent duplicate publishes are rejected.

## Try it with JanetCode

Run `make build` and set `OPENAI_API_KEY` for the default provider. Set
`OPENAI_BASE_URL` for an OpenAI-compatible endpoint. To use Anthropic, set
`PATCH_PUBLISH_PROVIDER=anthropic` and `ANTHROPIC_API_KEY`. The Docker bridge
allows network access for the model and tests, so use a disposable provider
credential and a runner whose reachable services are appropriate for the task.
Set `PATCH_PUBLISH_NETWORK=none` for offline fixtures. Set
`PATCH_PUBLISH_IMAGE` to use a trusted local image with Git and the project's
build tools.

Use a disposable bare repository with the commit to test under
`refs/pull/42/head`. The following setup models that ref using the current
repository's HEAD:

```sh
trial=$(mktemp -d)
git init --bare "$trial/target.git"
git push "$trial/target.git" HEAD:refs/pull/42/head
ci/patch-publish/run.sh prepare "$trial/target.git" 42 "$trial/proposal.patch" \
  "Make a small change to the README and run an appropriate check." --model MODEL
```

Review the patch and the agent's test output. Use `sha256sum "$trial/proposal.patch"`
to confirm the reviewed file matches the prepare receipt. Passing the receipt's
head and SHA-256 to `publish` approves those patch bytes:

```sh
ci/patch-publish/run.sh publish "$trial/target.git" 42 "$trial/proposal.patch" \
  REVIEWED_HEAD REVIEWED_SHA256
```

The publisher hashes its private copy of the patch, applies it to a temporary
Git index, and creates a proposal commit on a new `janetcode/pr-42/…` branch.
Git checks the PR head and creates the new branch in the same ref transaction.
Publication happens in the local bare repository supplied on the command line.
