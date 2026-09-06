// Run the real release shell steps with registry/dispatch commands stubbed out.
// No GitHub writes or container builds: node test-issue-1956-release-routing.js
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const vm = require('node:vm');
const { spawnSync } = require('node:child_process');

const read = name => fs.readFileSync(path.join(__dirname, '.github/workflows', name), 'utf8').replace(/\r/g, '');
const fast = read('release-fast-path.yml');
const deploy = read('deploy.yml');
let bash = process.env.BASH_PATH || 'bash';
if (!process.env.BASH_PATH && process.platform === 'win32') {
  // Prefer Git Bash over Windows' WSL launcher; CI uses the native Linux bash.
  const git = spawnSync('git', ['--exec-path'], { encoding: 'utf8' });
  const gitBash = path.resolve((git.stdout || '').trim(), '../../../bin/bash.exe');
  if (git.status === 0 && fs.existsSync(gitBash)) bash = gitBash;
}

// Extract known YAML blocks, retaining the actual expressions and shell code.
// Full YAML syntax is separately checked by actionlint; no YAML dependency here.
function block(source, key, indent) {
  const lines = source.split('\n');
  const prefix = ' '.repeat(indent) + key + ':';
  const start = lines.findIndex(line => line.startsWith(prefix));
  if (start < 0) return '';
  let end = start + 1;
  while (end < lines.length && (!lines[end].trim() || lines[end].search(/\S/) > indent)) end++;
  return lines.slice(start, end).join('\n');
}

function value(source, key, indent) {
  const raw = block(source, key, indent);
  if (!raw) return '';
  const lines = raw.split('\n');
  const first = lines[0].slice(indent + key.length + 1).trim();
  return first === '|' || first === '>'
    ? lines.slice(1).map(line => line.slice(indent + 2)).join('\n').trimEnd()
    : first;
}

const steps = source => source.split(/(?=^      - name:)/m).slice(1);
function evaluate(expression, context) {
  if (!expression) return true;
  return vm.runInNewContext(expression.replace(/^\$\{\{|\}\}$/g, '').trim(), {
    ...context, startsWith: (text, prefix) => text.startsWith(prefix)
  });
}
const expand = (script, context) => script.replace(/\$\{\{(.*?)\}\}/g, (_, expression) => String(evaluate(expression, context)));
const bashPath = file => process.platform === 'win32' ? file.replace(/\\/g, '/').replace(/^([A-Za-z]):/, (_, drive) => '/' + drive.toLowerCase()) : file;

function runSteps(source, context, edge, mutateFails = false) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'corescope-release-test-'));
  const output = path.join(dir, 'output');
  const log = path.join(dir, 'commands');
  fs.writeFileSync(log, '');
  fs.mkdirSync(path.join(dir, 'cmd/decrypt'), { recursive: true });
  const stubs = `
    log() { node -e 'require("fs").appendFileSync(process.env.COMMAND_LOG, JSON.stringify(process.argv.slice(1))+"\\n")' "$@"; }
    crane() {
      if [ "$1" = config ]; then
        [ "$EDGE_CONFIG" != missing ] || return 1
        printf '%s' "$EDGE_CONFIG"
      else
        log crane "$@"
        [ "$1" != mutate ] || [ "$MUTATE_FAILS" != true ]
      fi
    }
    gh() { log gh "$@"; }
    tar() { log tar "$@"; }
    go() { log go "$GOOS" "$GOARCH" "$CGO_ENABLED" "$@"; }
    jq() {
      node -e 'const fs=require("fs"); const assert=require("assert/strict"); assert.equal(process.argv[1], ".config.Labels[\\"org.opencontainers.image.revision\\"] // \\\"\\\""); console.log(JSON.parse(fs.readFileSync(0,"utf8")).config.Labels["org.opencontainers.image.revision"] || "")' "$2"
    }
  `;
  try {
    for (const step of steps(source)) {
      const script = value(step, 'run', 8);
      if (!script || !evaluate(value(step, 'if', 8), context)) continue;
      fs.writeFileSync(output, '');
      const result = spawnSync(bash, ['--noprofile', '--norc', '-e', '-o', 'pipefail'], {
        input: stubs + '\n' + expand(script, context), cwd: dir, encoding: 'utf8', timeout: 15000,
        env: {
          ...process.env, GITHUB_REF: context.github.ref, GITHUB_SHA: context.github.sha,
          GITHUB_OUTPUT: bashPath(output), COMMAND_LOG: bashPath(log), TMPDIR: bashPath(dir),
          EDGE_CONFIG: edge === null ? 'missing' : JSON.stringify({ config: { Labels: { 'org.opencontainers.image.revision': edge } } }),
          MUTATE_FAILS: String(mutateFails)
        }
      });
      if (mutateFails && result.status !== 0) return { commands: commands(), failed: true };
      assert.equal(result.status, 0, `${value(step, '- name', 6)}: ${result.error || result.stderr}`);
      const id = value(step, 'id', 8);
      if (id) context.steps[id] = { outputs: Object.fromEntries(fs.readFileSync(output, 'utf8').trim().split('\n').filter(Boolean).map(line => {
        const at = line.indexOf('=');
        return [line.slice(0, at), line.slice(at + 1)];
      })) };
    }
    return { commands: commands(), failed: false };
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
  function commands() { return fs.readFileSync(log, 'utf8').trim().split('\n').filter(Boolean).map(line => JSON.parse(line)); }
}

function context(ref = 'refs/tags/v9.8.7', event = 'workflow_dispatch', inputs = {}) {
  return { github: { ref, ref_name: ref.split('/').pop(), sha: 'a'.repeat(40), repository: 'example/corescope', event_name: event }, inputs, steps: {}, needs: {}, vars: {} };
}

// Honor the real job conditions AND implicit success() for needs. This catches
// release-artifacts accidentally depending on the skipped image or E2E jobs.
function route(ctx, failedJob) {
  for (const name of ['changes', 'go-test', 'e2e-test', 'build-and-publish', 'release-artifacts', 'deploy', 'publish']) {
    const job = block(deploy, name, 2);
    assert.ok(job, `missing ${name} job`);
    const needs = value(job, 'needs', 4).replace(/[\[\]\s]/g, '').split(',').filter(Boolean);
    const run = needs.every(need => ctx.needs[need].result === 'success') && evaluate(value(job, 'if', 4), ctx);
    ctx.needs[name] = { result: run ? (name === failedJob ? 'failure' : 'success') : 'skipped', outputs: { code: 'true' } };
  }
  return ctx.needs;
}

for (const [name, edge] of [['matching', 'a'.repeat(40)], ['missing', null], ['mismatched', 'b'.repeat(40)]]) {
  const ctx = context();
  const { commands } = runSteps(block(fast, 'retag-or-fallback', 2), ctx, edge);
  const dispatches = commands.filter(command => command[0] === 'gh');
  assert.equal(dispatches.length, 1, `${name}: tag must dispatch the artifact workflow exactly once`);
  const dispatch = dispatches[0];
  assert.deepEqual(dispatch.slice(1, 4), ['workflow', 'run', 'deploy.yml']);
  assert.equal(dispatch[dispatch.indexOf('--ref') + 1], ctx.github.ref, `${name}: dispatch must preserve the tag source`);
  assert.equal(dispatch[dispatch.indexOf('--repo') + 1], ctx.github.repository);
  const matching = name === 'matching';
  assert.equal(dispatch.includes('images_published=true'), matching);
  if (!matching) assert.ok(!dispatch.includes('--field') && !dispatch.includes('-f'), 'old-tag fallback must not require new workflow inputs');
  assert.equal(commands.filter(command => command[0] === 'crane' && command[1] === 'mutate').length, matching ? 1 : 0);
  assert.deepEqual(commands.filter(command => command[0] === 'crane' && command[1] === 'tag').map(command => command.at(-1)), matching ? ['v9.8', 'v9', 'latest'] : []);
  const jobs = route(context(undefined, undefined, { images_published: matching }));
  assert.equal(jobs['release-artifacts'].result, 'success', `${name}: release artifacts must run`);
  assert.equal(jobs['go-test'].result, 'success', `${name}: release still requires Go validation`);
  for (const job of ['e2e-test', 'build-and-publish']) assert.equal(jobs[job].result, matching ? 'skipped' : 'success', `${name}: ${job}`);
  assert.equal(jobs.deploy.result, 'skipped');
  assert.equal(jobs.publish.result, 'skipped');
  console.log(`PASS ${name} edge: one artifact dispatch, correct image route`);
}

const failedRetag = runSteps(block(fast, 'retag-or-fallback', 2), context(), 'a'.repeat(40), true);
assert.equal(failedRetag.failed, true);
assert.equal(failedRetag.commands.filter(command => command[0] === 'gh').length, 0, 'failed retag must not dispatch with images_published=true');

for (const [ref, event] of [['refs/heads/master', 'push'], ['refs/heads/master', 'workflow_dispatch'], ['refs/pull/1/merge', 'pull_request']]) {
  const jobs = route(context(ref, event, { images_published: true }));
  assert.equal(jobs['release-artifacts'].result, 'skipped', `${event}: no GitHub release`);
  assert.equal(jobs['build-and-publish'].result, 'success', `${event}: tag-only input must not skip branch/PR checks`);
  const publishing = steps(block(deploy, 'build-and-publish', 2)).find(step => step.includes('uses: docker/build-push-action'));
  assert.equal(Boolean(evaluate(value(publishing, 'if', 8), context(ref, event))), event === 'push', `${event}: GHCR publishing`);
}
assert.equal(route(context(), 'go-test')['release-artifacts'].result, 'skipped', 'failed Go validation must block release');
const dispatchInput = block(deploy, 'images_published', 6);
assert.equal(value(dispatchInput, 'type', 8), 'boolean', 'dispatch flag must retain boolean semantics');
assert.equal(value(dispatchInput, 'default', 8), 'false', 'manual and fallback dispatches must build images by default');

const release = block(deploy, 'release-artifacts', 2);
const builds = runSteps(release, context(), null).commands.filter(command => command[0] === 'go');
assert.deepEqual(builds.map(command => command.slice(1, 4)), [['linux', 'amd64', '0'], ['linux', 'arm64', '0']]);
for (const command of builds) assert.ok(command.includes('-ldflags=-s -w -X main.version=v9.8.7'), 'binary version must come from tag');
const upload = steps(release).filter(step => step.includes('uses: softprops/action-gh-release@v2'));
assert.equal(upload.length, 1, 'publish both architectures together, before the release becomes immutable');
assert.equal(value(upload[0], 'fail_on_unmatched_files', 10), 'true', 'missing assets must prevent publication');
assert.deepEqual(value(upload[0], 'files', 10).trim().split('\n').map(line => line.trim()), ['corescope-decrypt-linux-amd64', 'corescope-decrypt-linux-arm64']);
assert.equal(value(upload[0], 'draft', 10), '', 'standard release action must finalize after both uploads');
assert.equal(value(upload[0], 'prerelease', 10), '', 'standard release action must upload before publishing');
const checkout = steps(release).find(step => step.includes('uses: actions/checkout@'));
assert.equal(value(checkout, 'ref', 10), '', 'checkout must retain the dispatched tag/SHA');
assert.ok(!value(block(deploy, 'push', 2), 'tags', 4), 'fast path must remain the sole tag-triggered image writer');
console.log('PASS failed retag/Go gates, branch/PR routes, and complete tagged release assets');
