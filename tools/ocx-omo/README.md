# ocx-omo

omo가 업데이트되면 내장 카테고리·에이전트 모델 순서를 다시 읽어서, OpenCodex 모델 기준의 새 라우팅을 제안하는 도구입니다.

LINA에서는 `lina omo routing plan|apply|check`가 이 도구를 LINA의 고정 Node로 실행합니다(lina#34). 직접 쓸 때는 Node 24 이상에서:

```sh
npm ci
npm run build       # tsc → dist/
npm test            # 빌드 후 node --test

node dist/src/cli.js plan        # 제안 라우팅과 현재 omo.jsonc의 차이 보기
node dist/src/cli.js apply       # 백업 후 ~/.omo/omo.jsonc에 쓰고 기준선 저장
node dist/src/cli.js check       # omo 업데이트 후 실행. 0 = 변경 없음, 10 = 검토할 변경 있음, 2 = 오류
```

`check`가 변경을 찾으면 경로별 전후 차이와 함께 `ocx-omo apply --only <routes>` 명령을 알려줍니다. 자동으로 쓰지는 않습니다.

## 라우팅 규칙

- 소유하는 키는 라우트의 `model`, `models`, `reasoning`, `variant`, `reasoningEffort`, `fallback_models`뿐입니다. 주석과 다른 설정은 유지됩니다.
- 모델 후보는 `policy.default.json`의 `namespaces` 순서로 찾습니다. 이 목록에 없는 경로는 쓰지 않습니다.
- 업스트림의 `x-fast`는 OpenCodex의 `x--fast`로 바꿉니다. `exclude`의 id는 제외합니다.
- 같은 모델은 처음 나온 것만 남기고, 추론 강도는 모델이 지원하는 값으로 내립니다.
- `extras`는 라우트별로 업스트림 순서 뒤에 붙입니다.

정책을 바꾸려면 `policy.default.json`을 `~/.omo/ocx-omo/policy.json`으로 복사해서 수정합니다.

## magpie 모드 (lina#587)

노드가 magpie로 모델을 받으면(`worker.proxy.kind: magpie`) LINA가 `--proxy magpie --remote-id <id> --names <path>`를 붙여 실행합니다. 직접 쓸 때는:

```sh
node dist/src/cli.js plan --proxy magpie --remote-id hub --names ../../hermes_cli/worker/proxy_names.json
```

- 카탈로그는 `models.json`의 `providers.magpie.models`(id 예: `hub/claude/claude-opus-5-5`)입니다. magpie의 omo 연결 명령이 씁니다.
- 같은 정책으로 같은 라우트를 만듭니다. omo의 추천 순서와 추론 강도, `extras`는 그대로이고, 각 OCX 후보(`namespaces` 순서)를 이름표(`--names`, LINA의 `hermes_cli/worker/proxy_names.json` 하나, 복사본 없음)로 magpie provider에 옮겨 `magpie/<remote_id>/<provider>/<id>`로 씁니다. 노드 자신의 magpie가 모델을 가진 경우(`--remote-id ""`, lina#623)에는 `magpie/<provider>/<id>`로 씁니다.
- 두 OCX 네임스페이스가 같은 provider가 되면(`google`, `google-antigravity` → `antigravity`) 체인마다 처음 것만 남습니다.
- 카탈로그에 없는 모델(`-fast` 티어 등)과 이름표에 없는 네임스페이스는 OCX에 없는 모델처럼 빠지고 `not routed`에 나옵니다. `extras`의 모델도 같습니다.
- `check` 종료 코드(0/10/2)와 기준선 형식은 같습니다. 모드를 바꾸면 모든 라우트가 바뀐 것으로 나오고(10), 다시 `apply`하면 됩니다. magpie로 적용한 뒤 OCX로 다시 적용하면 원래 라우트가 그대로 돌아옵니다(백업은 남음).

## 다른 컴퓨터에 세팅할 때

1. `ocx integration client enable --client omo`로 `providers.opencodex` 카탈로그를 받습니다. 카탈로그가 오래되면 새로 켠 모델이 제안에 나오지 않습니다.
2. `ocx-omo plan`으로 확인한 뒤 `ocx-omo apply`를 실행합니다.
3. 깨끗한 HOME에서 위임이 `open_failed`로 실패하면 `OMO_CODING_AGENT_DIR=$HOME/.omo/agent`를 지정합니다. 데스크톱 런타임으로 확인한 현상이고, npm 설치판은 확인하지 않았습니다.
