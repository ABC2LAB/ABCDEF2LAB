# 통제 조회 저장 객체의 읽기 전용 진단

## 상태와 호출

선행 #159 head `32bd87f77460953f2a60a5de3cdc391cc0b75758` 기반의 명시적인 trusted Go 진단이다.
`(*Server).InspectControlledReadStorage(ctx)`는 실제 `s.m.pg.DB`만 사용한다.
기본 startup·HTTP·CLI·등록·전송·최종화에는 연결하지 않는다. 응답에 종합 ready/허용 판정은 없다.

진단 목적은 migration 누락·다른 column type·현재 role의 table 권한 사실을 실행 전에 관찰하는 것이다.
이 결과가 실제 실행이나 finding 저장 성공, 승인·계정·KG·harness·키/epoch/root 신뢰를 증명하지 않는다.
외부 endpoint를 추가하려면 별도 운영자 인증 계약과 구현이 필요하다.

## 고정 검사 범위

이 단위는 다음 16개 관계와 직접 SQL이 쓰는 고정 column/builtin type 목록만 검사한다.
범위는 해당 head의 `auditcheckpoint/store.go`, `evidencerevision` 저장 SQL,
`db/e3_findings.go` 및 관련 schema의 직접 참조에서 정했다. 그 함수들이 부르는 모든 helper의
추가 의존성까지 검사하는 목록은 아니다.

- CPD: audit_checkpoint_epochs/history/heads/operations.
- revision: evidence_revision_initials/operations.
- 최초 E③: e3_evidence_manifests, e3_oracle_verdicts, e3_verdict_issuances, e3_verdict_links.
- 직접 결속/저장 기반: tasks, explorations, task_control_modes, exploration_nodes,
  exploration_edges, findings.

새 SQL 객체·새 key registry 검사·migration·수리·쓰기 권한 부여는 하지 않는다.
응답의 관계/column/privilege 이름은 고정 검사 목록이며 실제 row payload·원시 key·epoch·관찰·
credential·현재 role 이름·DSN·실제 schema 이름·원시 DB 오류는 읽거나 반환하지 않는다.

## 실제 관찰의 의미

실제 DB pool에서 한 연결을 빌려 3초 context deadline을 적용한 read-only repeatable-read
transaction으로 `pg_catalog`만 읽고 rollback한다. caller의 더 짧은 deadline도 적용된다.
이는 연결/rollback 정리까지 포함한 벽시계 반환 SLA가 아니다. DDL/DML·row lock·
sequence allocation·migration·commit을 하지 않는다. 실패/취소/timeout/rollback 오류는
고정 사유와 빈 report로 반환하며 일부 결과를 정상 report로 반환하지 않는다.

`resolution_basis=inspected_connection_search_path`는 **그 연결의 실제 search_path**로
고정된 비한정 관계 이름을 해석했다는 뜻이다. 실제 저장 SQL도 비한정 이름을 쓰지만 다른 pool 연결이나
나중 실행의 search_path가 같거나 신뢰된다고 보증하지 않는다. search_path를 바꾸거나 schema를 지정해
검사를 통과시키지 않는다.

관계 이름을 해석하는 to_regclass는 이 search_path를 유지하지만, catalog의 함수·type·비교/정렬
연산자는 pg_catalog로 한정한다. 사용자 schema가 pg_catalog 앞에 있어도 동일 signature의
사용자 정의 연산자를 진단 쿼리가 실행하지 않게 한다. 이것이 대상 search_path 자체를 신뢰시키지는 않는다.

`name_observed_in_catalog`는 같은 고정 이름의 catalog entry가 관찰됐다는 사실이다.
`resolved=false`는 현재 연결에서 이름이 해석되지 않았다는 뜻이며, 객체 부재나 권한 거부로 단정하지 않는다.
다른 schema의 동명 객체, search_path 밖의 객체, schema USAGE 부족을 구분해 확정하지 않는다.
이때 ordinary_table/column/privilege 결과는 생략한다. 실제 catalog/qualification/권한 조회 오류는
부재로 바꾸지 않고 전체 진단 실패로 반환한다.

해석된 객체에는 ordinary table 여부, 필수 column 존재와 고정 builtin type OID 일치만 보고한다.
domain·type modifier·nullability/default·constraint/index·trigger 의미나 RLS를 검증하지 않는다.
다른 종류의 관계도 table로 단정하지 않는다. 동시 DDL 이후/미래의 사용 가능성도 보증하지 않는다.

SELECT·INSERT·UPDATE는 각각 별도 `has_table_privilege` 호출로 보고한다. 여러 privilege를
쉼표로 넘긴 결과를 모두 허용됐다는 뜻으로 쓰지 않는다. PostgreSQL의 해당 함수는 쉼표 목록에 ANY
의미를 적용한다. [PostgreSQL 16 권한 조회 문서](https://www.postgresql.org/docs/16/functions-info.html#FUNCTIONS-INFO-ACCESS-TABLE)

이는 **table-level 권한** 사실이다. false가 곧 실제 쿼리 불가능이라는 뜻은 아니다. column-level grant,
RLS, trigger/security-definer, sequence 권한은 별도다. 예를 들어 row-locking SELECT는 column-level
UPDATE로도 허용될 수 있다. [PostgreSQL 16 SELECT 문서](https://www.postgresql.org/docs/16/sql-select.html)
어떤 table 권한 조합도 실행 권위나 올바른 정책으로 해석하지 않는다.

고정 `unassessed`는 위 항목 외에도 host/root/epoch/key 신뢰, search_path 신뢰와 다른 연결,
mode/archive 및 다른 helper 의존성, 간접 notification 저장, 내구성과 미래 실행을 명시한다.
예를 들어 helper가 쓰는 task의 추가 상태 column이나 task_archives까지 검사했다는 뜻은 아니다.
진단이 actual authority lease를 발급하지 않는다.

## 검증과 제한

wrapper가 실제 CID/name/label을 확인한 소유 폐기용 PG16에서만 fixture schema와 NOLOGIN role을 만든다.
운영 DB·host 계정/권한은 바꾸지 않는다. 실제 ARTEX 및 CPD/revision migration을 적용한 schema와
누락/부분/wrong-type fixture, search_path 우선순위·schema USAGE 부족, 개별 table 권한과 column grant,
취소/deadline·실제 pool 대기 중 내부 context deadline 취소, 잘못된 config를 검사한다.
네트워크 장애나 rollback 정리의 최대 시간을 실측한 시험은 아니다. 사용자 schema의 실제 text=text 연산자가
대조 쿼리에서는 예외를 내지만 한정된 진단 쿼리에서는 실행되지 않는 것도 검사한다.

side-effect view의 sequence가 증가하지 않고, 실제 driver가 read-only BEGIN/SELECT/rollback만
수행하는지 검사한다. 이 검증은 실제 DB catalog 조회이며 mock 성공으로 계산하지 않는다.
호출의 실제 table payload 평가·키 읽기·전원 장애·운영 격리·실제 Juice Shop 회차는 검증하지 않는다.
fixture의 합성 권한과 실제 운영 권위는 구분한다.
