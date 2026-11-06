# 과거 Guard 표식

`guard-audit-marker`는 과거 기록을 읽기 위한 지표다. 현재 엔진이 이 값을 새로
출력한다고 해석하지 않는다. 원문 출처는 ABC2LAB/ABCDEF2LAB 커밋
`450cc71f00092e7788a228a96b3bc71431ec288d`의 `guard/guard.go`,
`systemBlockMessage`다.

```text
【ARTEX 平台管控·非目标防御】
```

이 표식은 모델에 돌려주는 차단 결과에 붙었다. 과거 버전의 일부 감사 경로에도
같은 결과가 저장됐다. 현재 감사 기록은 표시용 사유를 따로 보관하므로 표식이
없을 수 있다. 표식 하나만으로 실행이나 도구의 실제 전송 성공을 확정하지 않는다.

현재 표식은 `[ARTEX PLATFORM CONTROL - NOT TARGET DEFENSE]`다.
Sigma 규칙과 합성 시험은 두 표식을 모두 유지한다. 기존 CSV 지표 ID와 MISP
속성 UUID는 과거 표식에 유지하고, 현재 표식은 `guard-result-marker`로 추가한다.
