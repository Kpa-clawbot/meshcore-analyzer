# CoreScope QA artifacts

Project-specific assets for the [`qa-suite`](https://github.com/Kpa-clawbot/ai-sdlc/tree/master/skills/qa-suite) skill.

## Layout

```
qa/
├── README.md                  ← this file
├── plans/
│   └── <release>.md           ← per-release test plans (one file per RC)
└── scripts/
    └── api-contract-diff.sh   ← CoreScope-tuned API contract diff
```

## How to run

```
qa staging              # use the latest plans/v*-rc.md against staging
qa pr 806               # use plans/pr-806.md if it exists, else latest plans/v*-rc.md
qa v3.6.0-rc            # use plans/v3.6.0-rc.md
```

The parent agent loads the qa-suite skill, which reads:
1. The plan file from `qa/plans/`
2. Bundled scripts from `qa/scripts/`
3. The reusable engine + qa-engineer persona from the skill itself

## Adding a new plan

For each release candidate, copy the latest `plans/v*-rc.md` to `plans/<new-tag>.md` and update:
- The commit-range header (`vN.M..master`)
- Any new sections for new features in the release
- The "Test data" section if new fixture types are needed
- The GO criteria (which sections are blockers)

## Adding a new script

Custom scripts go in `qa/scripts/` with `mode=auto: <script-name>` referenced from the plan. The qa-engineer subagent runs them with two args: `BASELINE_URL TARGET_URL`.

Authoring rules from the qa-suite skill:
- 4-way error classification: `curl-failed` / `parse-empty` / `shape-diff` / field-missing
- Distinguish HTTP errors from jq parse failures
- Don't silence stderr — script bugs must surface
- Exit code = number of failures
