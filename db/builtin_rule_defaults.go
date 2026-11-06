package db

import (
	"crypto/sha256"
	"fmt"
)

// e53a79f 및 언어 변경 직전 base 6551c59의 기본값을 정확히 대조한다. 원문은 해시로만 보관한다.
type legacyRuleTextHash struct{ Name, Message string }

var legacyRuleTextHashes = map[string]legacyRuleTextHash{
	"(?i)\\brm\\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)": {"7143ae56b7d7da6daf1075604294be15d5d24a8e46e2a860976ee37171794a4f", "1be41e1a54ea8451187ed6ee5cd1562d002b866c7172d122ee30779e5b03b727"},
	"\\brm\\b[^\"'\\n]{0,60}[\"'\\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)":     {"d3465270f084947f23d16803d644e5a8bcd2e854b59ad83f0b181451ef63bd20", "54f628583c057573ab4f25412d5b3533765e469d4ba2c595311afb6aa4d0940d"},
	"\\bmkfs\\b": {"4c59c629b9bed4a92afd7905a5276724acc15cb36c5b1e2069bb6715952c5d5a", "a3a7ddb8c3e486b280bab3bfe5a08665caa7ae576e31d629f3a25e8499b55027"},
	"\\bdd\\b[^|\\n]{0,100}\\bof=\\s*/dev/[a-zA-Z]":                               {"60be70ef1b701cd85fd25b9754581bc39483805ef80b051b198ebc6c7ea79d93", "69ccff03b107b9e75e30137330c278c5ac478494408e6e672e6b5cad45e0d9d6"},
	":\\(\\)\\s*\\{[^}]*:\\|:":                                                    {"1a54f3da4f33c328599541a6a2adf6a980dacd7a8b4de4047d4f128e91ff8d3b", "3f1ddef3f515329f3adbeed89725920bf6c491fecf1d36ade2c755c1c4301ea9"},
	"\\b(?:shutdown|reboot|halt|poweroff|init\\s+[06])\\b":                        {"93eea2f1b0df37608a67915aff360897058fc050ad0e323db7f101b394fbb2c3", "b57286d3d25c15bb10fd28151a2004f12a88e7c52a81070749e923dde091297b"},
	"\\bkill\\s+-9\\s+-1\\b|\\bkillall\\s+-9\\b":                                  {"3971efd08602cc2c9c85591fff004803d6a0494c9606652cb69a6231d9a432bf", "6cc340eec68b1c1409c8a2f94001e61b9c0adbd98885271c2cdc58e72dddf858"},
	"\\b(?:shred|wipe)\\b[^|\\n]{0,80}/dev/[a-zA-Z]":                              {"43aabb89427515615b5af3b6543abb0599f087c9375dd76ba99e673338b9a64e", "8188a4365a867991008492553a339984975506172ec65c483ac08f95b20e70e2"},
	"\\biptables\\s+(?:-F|--flush)\\b|\\bnft\\s+flush\\s+ruleset\\b":              {"d90d975a8f45c905f3a67d6fabe20d27e9c5abecb38ca69f120ad9c3bd40f874", "c301075fa3ec31ab6c056b92ed3ab403dd06f1fd9d0e8e896ab052e133f2f8e8"},
	"(?i)\\bDROP\\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\\b": {"0e89d27a14e79ced281d85cde38bcc04b1c976385bcf9cdb3397b3bca69deb66", "457d111465afce289d5d65e15d340ccb8ce2c2dee029e086bbfdfa614fbd9bb6"},
	"(?i)\\bTRUNCATE\\s+(?:TABLE\\s+)?\\w":                                        {"1e4dde9b9a57038f81485cb324f1ee53dcc6a2b42b49593876ec720af5450642", "3a042aaef637fbdf85479384776f094636c5b60363c9eade5c12087d533e9a18"},
	"(?i)\\.(?:dropDatabase|dropCollection|drop)\\s*\\(":                          {"ee419355a4de286997acdf731ff4aa4e01f2ff29aee2f468f68d9bf9d683bcf3", "d6caf115c47db975f6d0079fd50f3bf6c1034f7a23fe3167ac059c5198f9f941"},
	"(?i)\\b(?:FLUSHALL|FLUSHDB)\\b":                                              {"47440c96f2902fa10e9abb2e30df1100f6117f41896426236f7e10d6304505a3", "75ef44fb5e52e855daeb73866518011fbc7c22d0c8bc27952d1003f24f7ee961"},
	"(?i)\\bcurl\\b[^|\\n&;\"]{0,300}(?:-X\\s*DELETE|--request\\s+DELETE|-XDELETE)|\\bwget\\b[^|\\n&;\"]{0,300}--method[=\\s]+DELETE": {"bd41a16179b5ca5a07bc95cea5ac4bf0ba559ece4bd48bfd8ee1b4a00a6a447f", "d81e9bb751687e1f0169757eb3c1a15e0ade327e3c6002550306b2ef24fc6778"},
	"(?i)\\b(?:requests|httpx|aiohttp|urllib\\.request)\\.delete\\s*\\(|session\\.delete\\s*\\(|client\\.delete\\s*\\(":               {"2a6323ced68b6644ddde7c9a5395df2fd874b869a557e7259bf485adec1d7bd4", "8c8f0254b45da80c4593ae558635b86f8f8842be9b002b0b2aa4f6004e74872f"},
	"(?i)axios\\.delete\\s*\\(|method\\s*[:=]\\s*['\"]DELETE['\"]":                                                                    {"6222e3e359ab413677961e6a20238156dfdbce1d5ff841cbaca026d6eec45ccb", "be5b334621cd50cdb71b880b01f02cdadaf5a266af739348c2bba3c098ee4798"},
	"(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#\"'\\s]|$)":                            {"50d669a0adeb97902ec55b99552e211e30bda53d4edf5fb5b7d9b0da3539b6da", "e0e7c3f23a3a3bbb4738de838acbb14aa92ecab2babaf473c36b77d6b85848b7"},
	"(?i)\\b(rm\\s+-rf\\s+/|mkfs|dd\\s+if=|:\\(\\)\\s*\\{|shutdown|reboot|>\\s*/dev/sd)":                                              {"db1aad92f819d722df6eac6dbd1d7375a74e9983db1da5a297cf466a0eb80a5a", "36c557347a8961b2d9ae82aea7fca8fb6e0e10580bc28b5f5f6420e071e86364"},
	"(?i)(curl|wget|nc|ncat)\\b[^|]*\\b(\\|\\s*(curl|wget|nc))":                                                                       {"08ad70d9e7793f701e5febba2a58a8bc2a7bc567ad6442d3d0a1cf6427a80405", "4220bdfd82cb2075fb52c40089034d624f275e6cc9a8f445b233923a8419e194"},
	"(?i)/(?:(?:delete|remove|unlink|erase|destroy)[-\\w]*|del)(?:[/?#\"'\\s]|$)":                                                     {"1cf0167b330968b652d025047d00b18e3651a30be2fcc6babc1b163cc5866480", "00e6a386b8ec01eac7ea816b227f768ac8d79e3adac2ee45d4c4c1077e2f84e3"},
}
var legacyAssetNoteHashes = map[string]string{
	"fuzzy_domain\u0000.gov":    "0d6db59eab6c48d59dbedd32f4818a263cd8ed95f122926bd38bf283dcc42126",
	"fuzzy_domain\u0000.gov.cn": "5f913f06addea287eeb0c7ff8ae3f890ab3005f55a26532cd2002448cdf0ca7f",
	"fuzzy_domain\u0000.edu":    "54472835f5781699958308b8604a013b8e26985f14798f7a2f4c1f67b2dd3ef7",
	"fuzzy_domain\u0000.edu.cn": "db583aa30d2a49794685ea7173f197d3f0bbea5743ea4296ccc65ae8dc387522",
}

func builtinTextHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }
func builtinRuleTextSeeds() []interceptRuleSeed {
	out := append([]interceptRuleSeed(nil), defaultInterceptRules...)
	for _, r := range defaultInterceptRulesV2 {
		out = append(out, interceptRuleSeed{name: r.name, target: "tool_input", typ: "regex", pattern: r.pattern, action: r.action, message: r.message})
	}
	return append(out, interceptRuleSeed{name: deleteEndpointRuleName, target: "tool_input", typ: "regex", pattern: deleteEndpointPathPattern, action: "deny", message: deleteEndpointRuleMessage})
}

// 현재 또는 알려진 옛 기본값과 정확히 일치하는 규칙만 모델용 영어 설명을 쓴다.
// 이름·설명·동작을 운영자가 고친 규칙은 쓴 그대로 모델에 전달한다.
func BuiltinRuleModelMessage(r InterceptRule) (message, code string) {
	for _, seed := range builtinRuleTextSeeds() {
		if r.Pattern != seed.pattern || r.MatchTarget != seed.target || r.MatchType != seed.typ || r.Action != seed.action {
			continue
		}
		old, ok := legacyRuleTextHashes[seed.pattern]
		if !ok || (r.Name != seed.name && builtinTextHash(r.Name) != old.Name) || (r.Message != seed.message && builtinTextHash(r.Message) != old.Message) {
			continue
		}
		code = "builtin_rule_" + builtinTextHash(seed.pattern)[:12]
		return "A built-in platform safety rule blocked this operation (reason_code=" + code + ").", code
	}
	return "", ""
}
