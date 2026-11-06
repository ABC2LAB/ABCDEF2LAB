import { describe, expect, it } from "vitest";

import { zhToKo } from "./zh-ko";

describe("zhToKo", () => {
  it("translates an interrupted run summary", () => {
    expect(zhToKo("（运行被中断：用户暂停了任务；停在工具执行阶段，已运行 3 轮 / 1m2s，未完成）")).toBe(
      "(실행 중단: 사용자가 작업을 일시중지함 · 도구 실행 단계에서 멈춤 · 3회 / 1m2s 실행 · 미완료)",
    );
    expect(zhToKo("（运行被中断：未能取得取消原因；停在执行过程中，未完成）")).toBe(
      "(실행 중단: 취소 사유를 확인하지 못함 · 실행 단계에서 멈춤 · 미완료)",
    );
  });

  it("keeps an unknown abort reason as written", () => {
    expect(zhToKo("（运行被中断：dial tcp: timeout；停在模型输出阶段，未完成）")).toBe(
      "(실행 중단: dial tcp: timeout · 모델 출력 단계에서 멈춤 · 미완료)",
    );
  });

  it("translates the budget and no-summary endings, even when the hint was cut at 80 characters", () => {
    expect(zhToKo("（达到运行预算上限(max_turns)，已收尾写回事实，已运行 12 轮；本次无文字总结）")).toBe(
      "(실행 한도(max_turns)에 도달 · 찾은 사실은 저장함 · 12회 실행 · 요약 없음)",
    );
    expect(
      zhToKo("（无文字总结，终态 model_error：模型或 API 调用失败（网络、鉴权、限流、供应商 5xx 等），重试用尽后…）"),
    ).toBe("(요약 없음 · 종료 상태 model_error: 모델·API 호출 실패(네트워크·인증·사용량 제한 등))");
  });

  it("translates fixed prefixes and keeps the rest of the line", () => {
    expect(zhToKo("执行出错: context deadline exceeded")).toBe("실행 오류: context deadline exceeded");
    expect(zhToKo("[模型] 허용")).toBe("[모델] 허용");
  });

  it("leaves other text untouched", () => {
    for (const s of ["", "3차 계획 수립", "Bash curl -s http://target/", "模型认为这是只读请求"]) {
      expect(zhToKo(s)).toBe(s);
    }
  });
});
