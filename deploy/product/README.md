# 제품 UI·Go 이미지

저장소 루트에서 실제 웹 UI와 Go 서버를 같은 소스로 빌드한다. 사전 빌드한 `dist` 바이너리는 필요 없다.

```bash
docker build -f deploy/product/Dockerfile --platform linux/amd64 \
  --build-arg VCS_REF="$(git rev-parse HEAD)" -t abc2lab:local .
```

[Docker 전용 ignore 규칙](https://docs.docker.com/build/concepts/context/#filename-and-location)에 따라 이 Dockerfile 옆의 `Dockerfile.dockerignore`가 제품 빌드 컨텍스트에서 개인 지침·설정·키·환경 파일·로그·기존 산출물을 제외한다. Node·Go 빌드 도구와 의존성을 받는 빌드 단계에는 네트워크가 필요하다. 완성된 이미지는 UI와 Go 실행 파일, 기존 Bash·브라우저 도구를 포함하고 UID 1000으로 실행한다. JWT 키는 `/app/keys`, 데이터는 `/app/data`에 둔다. 이미지 빌드는 DB·모델·전송 정책의 설치나 실제 진단 수용을 완료하지 않는다.

[공식 평가](../../docs/evaluation/official-run.md#4-입력-준비-세션-전-1회)는 기존 루트 `Dockerfile`로 baseline과 개선 후를 빌드한다. 루트 파일과 루트 `.dockerignore`는 이 제품 이미지 변경으로 바꾸지 않는다. 평가의 사전 컴파일·API 전용 엔진·도구 트리 대조·실행 UID 조건도 기존 절차를 따른다. 이 제품 이미지를 baseline 비교용 엔진으로 사용하지 않는다.
