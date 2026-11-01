# 헤르메스 인수인계

저장소: https://github.com/thisisjun786/hermes-macos-unlock (비공개)

## 목표와 현재 상태

물리 모니터를 뺀 맥에서도 헤르메스가 필요할 때 잠금을 풀고 CUA 및 FaceTime 작업을 이어갈 수 있도록, **독립적인 잠금 해제 패키지**를 만들었다.

소스 전달 범위에는 다음이 구현돼 있다.

- macOS AuthorizationPlugin, 관리자 helper, GUI 사용자 에이전트.
- 5초짜리 일회용 권한, 사용자·GUI 세션·부팅 바인딩, 재사용·동시 소비 방지.
- 실제 화면 상태 + 해당 권한의 소비 기록 + 두 종류 키체인의 검사를 통과해야 성공하는 요청 처리.
- SSH 브리지와 Hermes 플러그인 3개 도구. 등록 중 원격 연결하지 않아 맥이 꺼져도 Hermes 시작을 막지 않는다.
- 단계별 설치/활성화/실기 검증/확정/원복/제거. 확정 전 자동 원복과 외부 인증 규칙 변경 충돌 감지.

**실제 시스템에 설치하지 않았다. 실제 잠금 해제 성공을 확인한 상태도 아니다.** 현재 검증은 네이티브 빌드·격리 시험·읽기 전용 상태 확인까지다. 이 차이를 최종 보고에서 유지해야 한다.

## 가장 먼저 읽을 파일

1. `README.md`: 구현 범위와 한계.
2. `report.md`: 실제 실행한 검증과 미검증 항목.
3. `docs/INSTALL.md`: 서명부터 설치·활성화·원복까지 정확한 명령.
4. `docs/ACCEPTANCE.md`: 실기 완료 기준.
5. `research.md`: Apple의 API/키체인 제한과 화면·FaceTime 조사 근거.

## 현재 관측된 차단 조건

2026-10-24 리나맥에서 개발 빌드의 무인 키체인 검사는 `legacy=-25293`, `Data Protection=-34018`로 실패했다. 유효한 배포 서명 인증서도 발견되지 않았다. 개발 빌드는 ad-hoc 서명이다.

이 상태는 코드가 미리 감지해 `activate`를 거절하는 조건이다. 서명된 실제 앱의 entitlement와 키체인 상태를 확인하고 해결해야 한다. 이를 없애려고 키체인을 초기화하거나 기존 인증정보를 출력하지 않는다. SIP 해제, 전역 인증 허용, 상시 무조건 Allow는 구현 방향이 아니다.

기기 별칭은 과거 기록과 바뀐 적이 있다. 조사 당시 `agent-mac`은 macOS 27.0.1, `desktop`(지금의 `hub`)은 macOS 27.0.1이었다. 현재 `mac-cua` 기본 대상은 `desktop`(지금의 `hub`)이었다. 설치 전에 대상 별칭·실제 하드웨어·GUI 사용자·OS를 다시 확인한다.

## 이어서 할 일

1. 저장소의 고정 커밋을 받아 Linux `make test`, 대상 맥 `make native-test`를 실행한다. 출력에 실제 상태와 합성 상태를 구분한다.
2. 정상 Developer ID 서명과 GUI 에이전트 entitlement를 준비한다. 키체인 사전 검사를 해결하고 `health`가 `desktop_ready:true`를 반환하는지 확인한다.
3. `docs/INSTALL.md`의 staged install을 사용한다. 실제 인증 설정은 별도 `activate` 전까지 변경하지 않는다.
4. 독립 SSH 복구 연결을 유지한 상태에서 짧은 활성화 시험을 시작한다. 화면을 잠근 뒤 `verify-unlock`이 실제 잠김→해제와 권한 소비, 두 키체인을 확인해야만 `confirm`한다. 실패하면 원복한다.
5. 모니터와 Screen Sharing 연결을 모두 끊어도 화면이 유지되는지 시험한다. 필요하면 지속 가상 디스플레이를 구성하고 CUA 캡처/입력을 검증한다. 현재 보이는 Screen Sharing 가상 화면을 지속 화면으로 가정하지 않는다.
6. 사용자가 허용한 시험 수신으로 FaceTime 수락 UI와 통화 미디어를 확인한다. 이 패키지는 임의로 통화를 걸거나 받지 않는다.
7. 반복 잠금, 수동 비밀번호/Touch ID, 에이전트 중단, 요청 취소, 재시작을 검증하고 실제 결과를 보고한다. 초기 로그인/FileVault는 별도 경로다.

## Hermes에 추가하는 방법

저장소 루트가 Hermes standalone 플러그인이다. `plugins.enabled`를 기존 값과 병합하고, 대상 프로필의 환경에 `HERMES_UNLOCK_SSH_HOST`를 지정한다. 다른 프로필의 설정을 덮어쓰지 않는다.

```sh
hermes plugins install thisisjun786/hermes-macos-unlock --ref FULL_COMMIT_SHA --enable
```

등록되는 도구:

- `mac_unlock_status`: 상태 확인만 한다.
- `mac_unlock`: 한 번의 제한된 해제 요청을 보낸다.
- `mac_unlock_revoke`: 아직 사용하지 않은 권한을 취소한다.

모델 도구 인자로 임의 대상 호스트나 셸 명령을 받지 않는다. 운영자가 설정한 호스트만 사용한다. `desktop_ready:true`는 FaceTime 성공이나 재부팅 복구 성공을 뜻하지 않는다.

## 완료 보고 기준

빌드/합성 시험, 설치/서명, 실제 잠금 해제, 키체인, 모니터 없는 CUA, FaceTime 통화를 각각 확인하고 구분해서 보고한다. 설치나 실기 검증이 막히면 정확한 실패 코드와 조건을 남긴다. 소스가 빌드된다는 이유로 전체 무인 운영을 완료했다고 보고하지 않는다.
