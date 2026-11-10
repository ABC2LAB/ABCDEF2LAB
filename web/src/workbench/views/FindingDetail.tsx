"use client";

// 발견 상세 (보고서 · 점검 진행 공통). 원본 `findingHtml(f, host, ctx, vdNow)` 와 `fEp(f)` 를 JSX 로 옮긴 것.
// ctx: report = 보고서 상세 패널 · live = 진행 화면의 발견 패널 (판정 중 표시 · '보고서는 점검이 끝나면…' 안내).

import type { Finding, LiveVerdict } from "../data/findings";
import { cx } from "../lib/cx";
import { Icon } from "../ui/icons";
import { CopyButton, Ep, InspHead, Isec, Sev, Verdict, VerdictLive } from "../ui/parts";
import s from "../workbench.module.css";

/** 발견의 메서드·경로, 없으면 헤더. 원본 `fEp(f)`. */
export function FindingEp({ f }: { f: Finding }) {
  return f.m && f.p ? <Ep m={f.m} p={f.p} /> : <span className={s.p}>{f.hdr}</span>;
}

export function FindingDetail({
  f,
  host,
  ctx,
  vdNow,
}: {
  f: Finding;
  /** 점검 대상 host (머리 · 증거 예시의 Host: · 재현 curl) */
  host: string;
  ctx: "live" | "report";
  /** 진행 화면의 지금 판정 (보고서에서는 없음 → 보고서 판정 f.vd) */
  vdNow?: LiveVerdict;
}) {
  const pending = ctx === "live" && vdNow === "판정 중";
  const sure = f.vd === "확정";
  // 재현 curl 의 id: 두 화면이 함께 마운트돼 있어 ctx 로 구분 (원본 'repro-'+ctx)
  const cid = `repro-${ctx}`;
  return (
    <>
      <InspHead
        eyebrow={
          <>
            {f.type} · <span className={s.host}>{host}</span>
          </>
        }
        title={f.title}
        chips={
          <>
            <Sev k={f.sev} />
            {vdNow ? <VerdictLive v={vdNow} /> : <Verdict v={f.vd} />}
            {f.isNew && ctx === "report" ? <span className={cx(s.tag, s.new)}>신규</span> : null}
          </>
        }
      />
      <div className={s["vi-ep"]}>
        <FindingEp f={f} />
      </div>
      <Isec t="내용">
        <p>{f.what}</p>
      </Isec>
      {f.full ? (
        <>
          <Isec t="계정별 응답 비교">
            <table className={s.cmp}>
              <thead>
                <tr>
                  <th>요청 주체</th>
                  <th>응답</th>
                  <th>판정</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>
                    계정 A<small>주문 소유자</small>
                  </td>
                  <td>
                    <span className={cx(s.sc, s.s2)}>200</span>
                    <small>본인 주문</small>
                  </td>
                  <td>정상</td>
                </tr>
                <tr>
                  <td>
                    계정 B<small>관계없는 사용자</small>
                  </td>
                  <td>
                    <span className={cx(s.sc, s.s2)}>200</span>
                    <small>A의 표식 노출</small>
                  </td>
                  <td>
                    <span className={cx(s.ac, s.vu)}>취약</span>
                  </td>
                </tr>
                <tr>
                  <td>
                    비로그인<small>대조군</small>
                  </td>
                  <td>
                    <span className={cx(s.sc, s.s3)}>302</span>
                    <small>로그인으로 이동</small>
                  </td>
                  <td>정상 차단</td>
                </tr>
              </tbody>
            </table>
          </Isec>
          <Isec t="증거 · 예시(가림 표시)">
            <pre className={s.code}>
              <span className={s.cm}># 계정 B의 요청 (A의 주문)</span>
              {`\nGET /api/orders/1012 HTTP/1.1\nHost: ${host}\nCookie: session=`}
              <span className={s.rd}>[REDACTED]</span>
              {"\n\n"}
              <span className={s.cm}># 응답: A 주문 전용 표식 포함</span>
              {'\nHTTP/1.1 200 OK\n{\n  "order_id": 1012,\n  "buyer_note": "'}
              <span className={s.hl}>ABC2LAB-CANARY-7f3a</span>
              {'",\n  "address": "'}
              <span className={s.rd}>[REDACTED]</span>
              {'"\n}'}
            </pre>
            <p className={s.help}>
              실제 값 대신 A 주문 전용 표식이 B의 응답에서 보였습니다. 주소와 개인정보는 가려서 저장하지 않습니다.
            </p>
          </Isec>
        </>
      ) : (
        <Isec t="증거">
          <p>{f.ev}</p>
        </Isec>
      )}
      {pending ? (
        <Isec t="판정">
          <p>검증 에이전트가 확인 중입니다. 증거가 충분하면 확정, 부족하면 판단 불가로 나눕니다.</p>
        </Isec>
      ) : (
        <Isec t={f.basisL}>
          <ul className={cx(s.ilist, !sure && s.dash)}>
            {f.basis.map((b) => (
              <li key={b}>
                <Icon name={sure ? "check" : "info"} />
                <span>{b}</span>
              </li>
            ))}
          </ul>
          {sure && f.full ? (
            <p className={s.help}>세 가지 증거가 모두 있어 확정했습니다. 하나라도 없으면 판단 불가로 분류합니다.</p>
          ) : null}
        </Isec>
      )}
      {f.full ? (
        <Isec t="재현 방법">
          <ol className={cx(s.ilist, s.dash)} style={{ listStyle: "decimal", paddingLeft: 18, display: "block" }}>
            <li style={{ display: "list-item" }}>계정 B로 로그인해 세션을 얻습니다.</li>
            <li style={{ display: "list-item" }}>A의 주문 번호로 아래 요청을 보냅니다.</li>
            <li style={{ display: "list-item" }}>
              응답에 표식 <code>ABC2LAB-CANARY-7f3a</code>가 있으면 재현된 것입니다.
            </li>
          </ol>
          <div className={s.codewrap}>
            <pre className={s.code} id={cid}>
              {`curl -s https://${host}/api/orders/1012 \\\n  -H 'Cookie: session=<계정 B 세션>'`}
            </pre>
            <CopyButton targetId={cid} className={s.copy} />
          </div>
          <p className={s.help}>세션 값은 저장하지 않으니 직접 넣으세요. 보고서와 공유본에도 들어가지 않습니다.</p>
        </Isec>
      ) : null}
      {f.next ? (
        <Isec t="다음 단계">
          <p>{f.next}</p>
        </Isec>
      ) : null}
      {f.fix ? (
        <Isec t="조치 방안">
          <p>{f.fix}</p>
        </Isec>
      ) : null}
      {f.full && ctx === "report" ? (
        <Isec t="조치 및 재점검">
          <ol className={s.tl}>
            <li className={s.done}>
              <span>발견</span>
              <span>10/25 14:21</span>
            </li>
            <li className={s.done}>
              <span>확정</span>
              <span>10/25 14:23</span>
            </li>
            <li className={s.now}>
              <span>조치 대기 · 담당자 확인 중</span>
              <span />
            </li>
            <li>
              <span>재점검 · 아직 안 함</span>
              <span />
            </li>
          </ol>
          <p className={s.help}>재점검은 같은 요청 3건(계정 A, 계정 B, 비로그인)만 다시 보냅니다.</p>
        </Isec>
      ) : null}
      {ctx === "live" ? (
        <Isec>
          <p className={s.help}>보고서는 점검이 끝나면 만들어집니다. 그때 조치 담당과 재점검을 정할 수 있습니다.</p>
        </Isec>
      ) : null}
      <Isec t="분류">
        <div className={s.chips}>
          {f.cls.map((c) => (
            <span key={c} className={s.chip}>
              {c}
            </span>
          ))}
        </div>
      </Isec>
    </>
  );
}
