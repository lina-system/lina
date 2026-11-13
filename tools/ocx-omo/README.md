# ocx-omo

omo가 업데이트되면 내장 카테고리·에이전트 모델 순서를 다시 읽어서, magpie 카탈로그 기준의 새 라우팅을 제안하는 도구입니다. 모델 연결은 magpie만 사용합니다.

LINA에서는 `lina omo routing plan|apply|check`가 이 도구를 실행합니다. 직접 쓸 때는 Node 24 이상에서:

```sh
npm ci
npm run build
npm test

node dist/src/cli.js plan        # 제안 라우팅과 현재 omo.jsonc의 차이 보기
node dist/src/cli.js apply       # 백업 후 ~/.omo/omo.jsonc에 쓰고 기준선 저장
node dist/src/cli.js check       # 0 = 변경 없음, 10 = 검토할 변경 있음, 2 = 오류
```

`check`가 변경을 찾으면 경로별 전후 차이와 함께 `ocx-omo apply --only <routes>` 명령을 알려줍니다. 자동으로 쓰지는 않습니다.

## 모델 연결과 경로

magpie의 omo 연결 명령으로 `models.json`의 `providers.magpie.models`를 받습니다. 카탈로그가 오래되면 새로 연결한 모델이 제안에 나오지 않습니다.

```sh
node dist/src/cli.js plan --remote-id hub
node dist/src/cli.js plan --remote-id ""
```

- `--remote-id`의 기본값은 `hub`입니다. 카탈로그 id `hub/claude/claude-opus-5-5`는 `magpie/hub/claude/claude-opus-5-5`로 씁니다.
- 노드 자신의 magpie가 모델을 가진 경우에는 `--remote-id ""`를 사용합니다. 카탈로그 id `claude/claude-opus-5-5`는 `magpie/claude/claude-opus-5-5`로 씁니다.
- remote id에 `/`를 넣으면 오류로 종료합니다.

## 라우팅 규칙

- 소유하는 키는 라우트의 `model`, `models`, `reasoning`, `variant`, `reasoningEffort`, `fallback_models`뿐입니다. 주석과 다른 설정은 유지됩니다.
- 각 모델 후보는 정책의 `providers` 순서(`codex`, `claude`, `antigravity`, `grok`)로 찾습니다.
- 카탈로그에 없는 모델은 빠지고 `not routed`에 나옵니다. 모델 id는 그대로 사용합니다.
- 같은 모델은 처음 나온 것만 남기고, 추론 강도는 모델이 지원하는 값으로 내립니다.
- `extras`는 라우트별로 업스트림 순서 뒤에 붙입니다.

정책을 바꾸려면 `policy.default.json`을 `~/.omo/ocx-omo/policy.json`으로 복사해서 `providers`와 `extras`를 수정합니다. 기준선은 `~/.omo/ocx-omo/state.json`에 저장합니다. 이미 적용한 magpie 라우팅은 같은 카탈로그와 업스트림에서 변경 없이 유지됩니다.

`--home`, `--omo-task`, `--models`, `--config`, `--policy`, `--state`로 경로를 지정할 수 있고, `plan`과 `check`는 `--json` 출력을 지원합니다. 전체 옵션은 `ocx-omo --help`로 확인합니다.
