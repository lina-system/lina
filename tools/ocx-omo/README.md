# ocx-omo

omo가 업데이트되면 내장 카테고리·에이전트 모델 순서를 다시 읽어서, OpenCodex 모델 기준의 새 라우팅을 제안하는 도구입니다.

```sh
bun install
bun link            # ocx-omo 명령 등록

ocx-omo plan        # 제안 라우팅과 현재 omo.jsonc의 차이 보기
ocx-omo apply       # 백업 후 ~/.omo/omo.jsonc에 쓰고 기준선 저장
ocx-omo check       # omo 업데이트 후 실행. 0 = 변경 없음, 10 = 검토할 변경 있음, 2 = 오류
```

`check`가 변경을 찾으면 경로별 전후 차이와 함께 `ocx-omo apply --only <routes>` 명령을 알려줍니다. 자동으로 쓰지는 않습니다.

## 라우팅 규칙

- 소유하는 키는 라우트의 `model`, `models`, `reasoning`, `variant`, `reasoningEffort`, `fallback_models`뿐입니다. 주석과 다른 설정은 유지됩니다.
- 모델 후보는 `policy.default.json`의 `namespaces` 순서로 찾습니다. 이 목록에 없는 경로는 쓰지 않습니다.
- 업스트림의 `x-fast`는 OpenCodex의 `x--fast`로 바꿉니다. `exclude`의 id는 제외합니다.
- 같은 모델은 처음 나온 것만 남기고, 추론 강도는 모델이 지원하는 값으로 내립니다.
- `extras`는 라우트별로 업스트림 순서 뒤에 붙입니다.

정책을 바꾸려면 `policy.default.json`을 `~/.omo/ocx-omo/policy.json`으로 복사해서 수정합니다.

## 다른 컴퓨터에 세팅할 때

1. `ocx integration client enable --client omo`로 `providers.opencodex` 카탈로그를 받습니다. 카탈로그가 오래되면 새로 켠 모델이 제안에 나오지 않습니다.
2. `ocx-omo plan`으로 확인한 뒤 `ocx-omo apply`를 실행합니다.
3. 깨끗한 HOME에서 위임이 `open_failed`로 실패하면 `OMO_CODING_AGENT_DIR=$HOME/.omo/agent`를 지정합니다. 데스크톱 런타임으로 확인한 현상이고, npm 설치판은 확인하지 않았습니다.
