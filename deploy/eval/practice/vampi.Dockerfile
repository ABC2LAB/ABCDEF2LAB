# 연습 대상 VAmPI(프로토콜 §6 개선 반복 대상). 공식 비교에 쓰지 않는다.
# 원본은 GET /createdb 전까지 DB 파일이 없어 러너의 pre 사본을 뜰 수 없고 /users/v1도 500이다.
# 준비 단계를 러너에 새로 두지 않으려고 이미지 안에서 DB를 미리 채운다. 회차마다 같은 초기 상태가 된다.
FROM erev0s/vampi@sha256:0a5a224b6e14ae7da6a6ea265178ff71286ff903aec74adee98f660bb0e4ca12
RUN ["python", "-c", "from config import vuln_app, db; from models.user_model import User; ctx = vuln_app.app.app_context(); ctx.push(); db.create_all(); User.init_db_users()"]
