import { type ReactNode, useState } from "react";

import { Screen } from "../components/Screen";
import { TopBar } from "../components/TopBar";
import { MockBanner } from "../components/ui";
import { VerifyDomainModal } from "../components/VerifyDomainModal";
import { useApiResource } from "../lib/api";
import { type EngineTask, listTasks } from "../lib/engine";
import { isInternalUrl } from "../lib/net";
import { useSelection } from "../lib/selection";
import { useGo } from "../nav";
import { DEFAULT_TARGET_URL, type Severity } from "../types";
import FirstDiagnosisEntry from "./FirstDiagnosisEntry";

type Props = {
  /** "점검 시작"을 누르면 입력한 주소를 위로 올려요 (요약 · 대시보드에 반영) */
  onStart: (input: string) => void;
};

const SEV_ORDER: Severity[] = ["critical", "high", "medium", "low"];
const SEV_LABEL: Record<Severity, string> = { critical: "심각", high: "높음", medium: "중간", low: "낮음" };
const STATUS_LABEL: Record<string, string> = {
  created: "대기",
  running: "진행 중",
  queued: "실행 대기",
  paused: "일시중지",
  done: "완료",
  failed: "실패",
  timeout: "시간초과",
};
function dateLabel(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return `${d.getMonth() + 1}/${d.getDate()}`;
}

/** 진입 하단 최근 점검 — 실 /api/tasks 에서 최근 4건 (없으면 숨김) */
function RecentScans() {
  const go = useGo();
  const { selectTask, tasksVersion } = useSelection();
  const { data, loading, error } = useApiResource(listTasks, [tasksVersion]);
  const tasks: EngineTask[] = data?.tasks ?? [];
  if (loading || error || tasks.length === 0) return null;
  const recent = [...tasks].sort((a, b) => b.created_at.localeCompare(a.created_at)).slice(0, 4);

  return (
    <section className="recent" aria-labelledby="recent-title">
      <div className="recent-head">
        <span id="recent-title">최근 점검</span>
        <button type="button" className="lnk" onClick={() => go("history")}>
          전체 기록 {tasks.length} →
        </button>
      </div>
      <p className="hint">미분류 항목을 포함한 전체 발견은 보고서에서 확인해 주세요.</p>
      <div className="recent-list">
        {recent.map((t) => {
          const terminal = t.status === "done" || t.status === "failed" || t.status === "timeout";
          const f = t.findings ?? { critical: 0, high: 0, medium: 0, low: 0 };
          const topSev = SEV_ORDER.find((s) => (f[s] ?? 0) > 0);
          const classifiedTotal = SEV_ORDER.reduce((n, s) => n + (f[s] ?? 0), 0);
          const meta = [
            STATUS_LABEL[t.status] ?? t.status,
            dateLabel(t.created_at),
            classifiedTotal > 0 ? `위험도 분류 ${classifiedTotal}` : "",
          ]
            .filter(Boolean)
            .join(" · ");
          let resultBadge: ReactNode;
          if (topSev) {
            resultBadge = (
              <span className={`sev ${topSev}`}>
                {SEV_LABEL[topSev]} {f[topSev]}
              </span>
            );
          } else if (t.status === "done") {
            resultBadge = <span className="stag none">발견 목록 확인</span>;
          } else {
            resultBadge = <span className="stag none">{STATUS_LABEL[t.status]} · 미완료</span>;
          }
          return (
            <div className="recent-row" key={t.id}>
              <div className="recent-main">
                <div className="nm">{t.name?.trim() || "이름 없는 점검"}</div>
                <div className="meta">{meta}</div>
              </div>
              <div className="recent-acts">
                {terminal ? (
                  <>
                    {resultBadge}
                    <button
                      className="btn xs"
                      type="button"
                      onClick={() => {
                        selectTask(t.id);
                        go("report");
                      }}
                    >
                      보고서
                    </button>
                  </>
                ) : (
                  <>
                    <span className={`stag ${t.status === "running" ? "run" : "wait"}`}>
                      {STATUS_LABEL[t.status] ?? t.status}
                    </span>
                    <button
                      className="btn xs primary"
                      type="button"
                      onClick={() => {
                        selectTask(t.id);
                        go("live");
                      }}
                    >
                      이어 보기
                    </button>
                  </>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </section>
  );
}

/** ===== 진입 ===== */
export default function EntryScreen(props: Props) {
  const { mode } = useSelection();
  return mode === "first-diagnosis" ? <FirstDiagnosisEntry /> : <ArtexEntryScreen {...props} />;
}

function ArtexEntryScreen({ onStart }: Props) {
  const go = useGo();
  const [url, setUrl] = useState(DEFAULT_TARGET_URL);
  /** 소유 확인 팝업: 열릴 때마다 n을 올려서 새로 마운트 */
  const [verify, setVerify] = useState<{ url: string; n: number } | null>(null);

  const start = () => {
    onStart(url);
    const trimmed = url.trim();
    // 내부망이면 바로 진행, 외부면 DNS TXT 소유 확인
    if (isInternalUrl(trimmed) === true) go("target");
    else setVerify((v) => ({ url: trimmed, n: (v?.n ?? 0) + 1 }));
  };

  return (
    <Screen id="entry">
      <div className="frame">
        <TopBar current="entry" />
        <div style={{ padding: "56px 24px 48px", maxWidth: 600, margin: "0 auto", textAlign: "center" }}>
          <div style={{ textAlign: "left" }}>
            <MockBanner>
              이 ‘새 점검’ 흐름(진입 · 범위 확인 · 작업 설정)은 시안이에요. 실제 점검 시작(엔진 실행)은 연결 전이에요.
              아래 <strong style={{ color: "var(--warn)" }}>최근 점검</strong>은 실데이터예요.
            </MockBanner>
          </div>
          <h1 style={{ fontSize: 28, fontWeight: 700, letterSpacing: "-.6px", marginBottom: 26 }}>
            어떤 사이트를 점검할까요?
          </h1>
          <label className="fl" style={{ textAlign: "left" }} htmlFor="e-url">
            점검할 사이트 주소
          </label>
          <div className="row" style={{ marginBottom: 12 }}>
            <input
              className="inp"
              id="e-url"
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.nativeEvent.isComposing) start();
              }}
              aria-label="사이트 주소"
            />
            <button className="btn primary" type="button" onClick={start}>
              점검 시작
            </button>
          </div>
          <p className="hint" style={{ textAlign: "left" }}>
            본인이 점검 권한을 가진 사이트만 입력해 주세요. 범위와 통제 정책은 엔진 연동 후 설정할 수 있어요.
          </p>

          <RecentScans />
        </div>
      </div>

      {verify && (
        <VerifyDomainModal
          key={verify.n}
          url={verify.url}
          onClose={() => setVerify(null)}
          onNext={() => {
            setVerify(null);
            go("target");
          }}
        />
      )}
    </Screen>
  );
}
