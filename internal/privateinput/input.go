// 승인된 기존 공급자의 수명과 실제 검증 경계만 조립하며 권한을 발급하지 않는다.
package privateinput

import "errors"

var ErrUnavailable = errors.New("private_input_unavailable")
