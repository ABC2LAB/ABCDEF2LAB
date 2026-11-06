package server

import (
	"encoding/json"
)

// e53a79f 및 언어 변경 직전 base 6551c59의 기본값을 정확히 대조한다. 원문은 해시로만 보관한다.
type legacyToolHash struct{ Description, Schema string }

var legacyToolHashes = map[string]legacyToolHash{
	"add_company_scope":            {"c460db4b5834fb3c22d27937a2b87902cd9eaf4246e53e7e44a4081c5d6243f0", "a2576503fca196496fb810873d6eab4a438fc3baba8b403d48d5aca8906304b1"},
	"add_hint":                     {"629c6494669d5d5b53b951c70c949833dc1a885e91c85469caa6a9527b9ca61e", "81a3edf9b74a68161abf44177d878e074c2cd83029ffe3b481e4237d46e02c45"},
	"add_intent":                   {"6754abb7254a6a7ec1cf980f00a768d384d473f84d4c90b1e0446963c0a92767", "4f1e846d98a2d864aaba60343c43646724370f4a4373ec126cce39315bbb00c0"},
	"add_task_hint":                {"4eb1f206d86556f2ccc0a394e5058250a602848a109f217cbd00bd3338b72b7e", "8f1a0868a563f27a4377168d01e0221ff0536ffee3465979cde902f65b59cec1"},
	"add_task_scope":               {"23caa9d2e944fac4bd7f329bd5de45344a0633ed9f554759c4be753e5c9cc9a3", "6148afed45ca873053d7d49426c6dccffc04a0acfc3167783aee231365bdcb57"},
	"bind_finding_traffic":         {"e3ca4e11bf4064efe57ae573a90c73d7e9e8abf5185ae0c108c1984ae4d879af", "c6e20fef4f661a056dbcfae8017faa878acb05afcdc0c6645ab89d921ad0dbb1"},
	"create_custom_tool":           {"7bfdeabe156fc42becdab03079dab0f7d4d4aead08b0f782f537533121485a28", "133e4656acfc4bfcbb2c7af39f1799062b526f526088a07debd88275b5a96f55"},
	"create_mcp":                   {"e71f9d4f5647c8c57d7c0fbe1b86795060e3c31c76fd1d58122061d907b52595", "551c72ef5a9fc82e403bc743f66cef056218298f963c2b1c5aecb26a14fa580a"},
	"create_skill":                 {"40a5e60ab56cebb9f574dc9fb3bb6f2b418c8d8baa452ff441fbf32c1aa82d2e", "79ca2cee65e01d9fe85af95733cf5d1c2801559e76be555b942a37f9d8389360"},
	"delete_assets_by_host":        {"194b747997ca61c2b7a6929f61f7642dcc7e2ca24fb37c279fe6d6297ef0a1f0", "c8537929e7f4449cc5cdca4284432c48f953ce14cdfdca5f78f16113972723f6"},
	"expand_digest":                {"e0807f8e20e25f3f682a51c688160942e568ac80ca0d16819e7361a9abe68fb1", "7ccbb8680abca77b46d38ef17eadac5f63e6967a06ca4edddae75498f2a540cb"},
	"get_finding_retest_context":   {"c7d224d35a8f484dab0bf72adea727236364b11f7cafe1674f60d81c7985d876", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"get_finding_traffic":          {"9fbbf5633682ea7d6260cbbfcf20ffd0bf073d659123c8615cc79e753f38afd6", "1d9e37e5413e4536d3c03093734dbe93b66f73c611991d155ebb4066e1ee12c5"},
	"get_task_graph":               {"3f2787299dbfa6bb755a7653cb3cab0c00dece049f4c9a4203ebb105dff80ab8", "5ac12509110ede5be63ffacb351c9033692963b4f7fbaec9e010789c2848136a"},
	"get_task_node_detail":         {"a0dce2524d6ebc8dc5f94765419d398dd820811a1440169a6f406914a0c2804f", "63e322daf00af66c3bdb4760f0ba0c840e55c37702ae96e8f1bb9f4c180abd3d"},
	"get_task_worker_trace":        {"5c5e3a70e3c8f650b094fcf1296b53de4f7f1763574b68a9142787b2adb77c28", "e16e6db15ce1b49fa7f48f246fdf95f00914f3614e8ee608f2d6b6b43dfabca0"},
	"get_worker_output":            {"f25ffdc9580a39cb8f6aca6c3bde7860934b73b579157533e97ee85ee271f21e", "9debd13d7fbd68abb833e90e1255181b18b4e2b4608589eab9e316c24a2d373f"},
	"get_worker_trace":             {"5931721f9a063a608b1ad5d72c2ca48b0bd763d607769c4d2161f074c23835ec", "a8e4b3a384fb993a4e93c6a849abfa1530575d539497370fda4590dcc5df4ee4"},
	"goal_met":                     {"cba19394662c07ae525bcf3e21ead34b671a3e391c4e2a020518d4b096a0ac00", "fb8a1cce4d627271fec128df490217763a1789e5db23251b47b416be3373ced5"},
	"graph_overview":               {"541280dbe0192fe121e79d956d9bc41553fa2b317a344cf635ee602aa196ada3", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"insert_assets":                {"c377ce43215f3b6b573b21e301b27180e62a0cc2a480f7b41d54f61692167dbd", "8af3162953a7fecccadaa4591aeada0a7378e365f869977819f7baa49da352a8"},
	"kill_work":                    {"34036c67e6fa85c1abe66fb5983e390c97acc525b499d6cad20e907877002db9", "5b3c16008cf2f3b78d7ba2b858ecfaf0ed3623108f9e7de80a5c284c876eedff"},
	"list_assets":                  {"cf302317a50c3f326e7eaefcd0d7f038bac04424d84cf3deeeb602198aa11aff", "49780a5a9e31416ad279a5d82e0a091a9d9cb361b17b9afeb33299fa87578e21"},
	"list_companies":               {"100d9924f89e3c50313af8296dafbc16d802733995f32f09cb24709276d2d9a3", "989193dca6a1b0098cf897b6a0dd1461927981c03a383287c52f2a49cdcd80f4"},
	"list_facts":                   {"367be32e153ea77e11ab5d4fdc25b8e2a1a7b733cc8e4bbe20df592c322283e0", "fd95769c695eb8bbd5412720e745503c7412314865f99e6117e441536b194aed"},
	"list_findings":                {"1e25104858ba614987dee500ff60d67e1fe837910f38ea750d9e2d2e8312bc15", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"list_goals":                   {"8badb45c95ec41fd8208f127cf91db19072b586ce25f82893abe5f320aedd433", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"list_llm_profiles":            {"e4c1aac1216302ab483281f660ae6d528f6aa69fb76605d93fa8b27083aa98ef", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"list_task_findings":           {"989043941aa39a3eff03f1f9d8088c392301a16dfe113d465301f08f7ac55ac0", "5ac12509110ede5be63ffacb351c9033692963b4f7fbaec9e010789c2848136a"},
	"list_task_worker_traces":      {"db57bc2c72a4b4e53a9b625fd1158b117521c4104f7d4a7c244b299fb7f15c04", "5ac12509110ede5be63ffacb351c9033692963b4f7fbaec9e010789c2848136a"},
	"list_tasks":                   {"3d5515eb6136cead1b9dd4d3c11368051794ddc9a98c0012d883c7d9f59ac8c3", "efddc7bd8bbcef73a14eb1ace1ffdaec81e518ef1e13c1e9271d0b8acb694a49"},
	"list_untested_assets":         {"751b0466c5ae59415d9c4a375ea4fc271f7b558f08342c8616d2579b3ee8e0d5", "0d337d041cbe18f46e5231362d91e22748ac7555b5766748026f3a4fba4dd481"},
	"node_detail":                  {"15b983a6465d518528df5f0042ca1fbfde7d6f6a7e9a8072656c34b982426417", "485579fda1c67a87fe3c64f5c60a0e6e51b3707cfbf8203081cc439351fbef1d"},
	"pause_task":                   {"d37d81dbb74321da470ca15e9b2cc6a04f85538f4b1611ef7e05f7f2e11bf6b1", "6058a042c6985e7599de7b7da5ed071d10254068396cb577c600be6320567799"},
	"prove_goal":                   {"8a40e6de058ad1d43cb9a666b045beeb868f8ad0a04320e76edba6d80fbfad66", "ef7eee2ca11acbab5a87f2aa6e8b8e31feb5fde2c077a8bcf16275feaebd5815"},
	"record_fact":                  {"8b9446c8c11a2cd45f6f5c45bb541a732c399415a718b44b69479e4619078593", "18152162586d4490c1651838f1811074e847183b99db9d9b49cbe323def55941"},
	"record_finding_retest_result": {"15bf1adec32a2b40e515263b5e8ab882b1328669687138dbbcd6cf74d606623f", "63bbe3d27a96ee382de6d3ec0795e9db0ba76854cf29b025f613aca16bdc97e3"},
	"report_finding":               {"91675222f3d26a6fd171f6a7c1693c735d9985a7f93d16dcb77610f550d9655d", "b0d190c0a1dacc7282cb5e501a3ca64912a2908e72f8611a909330df492e29f2"},
	"search_all_worker_traces":     {"a5198e6bdabe3c86b660a542fe30464ce6846cb522f29312f687ae84a340a0ac", "4ad19f7a06001c1bb3d1e91de3b37623ed93b7ec52b921875333e40addcea372"},
	"search_task_worker_traces":    {"3a52371e00c54c2287ecdc0cd824faed79e4e409281d2b7a991b7fb619efcb34", "cc02120f5b50d1ad31028428f371591918de54e3764a611fefe4847fa9183827"},
	"set_constraints":              {"46a72ba15f4381fd8c8e2835ca5dee1c3054de81dde1209b5f913f5f8e68a41d", "0f1c225612e984b42c0067642a722cc6ab964bd9aede8eb5286cf4e7fe9109f3"},
	"set_goals":                    {"0115b98eabff22aea4710fe36255915c9de3b48cd451111da3074118ae83d1dd", "a86e4aa2517af52563c767c80ff366b0d38d95f5e8e3a4a9938da6036cf5892e"},
	"spawn_task":                   {"d79dc8da3dbfe456ec7ca051ad6751ff8dbf3e2a4db00e0bd26bdbc2631471a6", "9c3d471cedb044b3695e47f63469d53a86ba7223b16a89f7d56c49653c2d3152"},
	"steer_work":                   {"a7a86be80c94c7e3525493136124939428413a3311b5283afa66256da6b5e135", "b8af9ecbc011be98992ca412004f36d4d1bcc5963c42f6601f47737096bab6ac"},
	"traffic_blob":                 {"315f23469723e3c655c74e77f59e87608d750aebc2dce3b14619b24e1a3953c2", "3ee745920a6633c040f361d55a07504b4eecd05427297072fa28a90f770b8c1d"},
	"traffic_get":                  {"f530c0796b3822cde3bd18677d07b4189de5122dca6109ecbe8cd0e9a6e2f790", "fe9b7d35c8afb03c9cfe89f02e9be1493c8946ae1d56b95bffa2cea33f1224c9"},
	"traffic_search":               {"68cd3bfc0d83573b4696a2015b346f60d4950746d08b9658a1f930965d094b95", "b4fc267a72e14b3fd53e235f3ee2786736de51900349fda64c5fab6600839eb3"},
	"update_custom_tool":           {"f44f48e618598dd2a99e2a2e2cf355da52751dc5f74734616772158c13a9e50c", "0c9a63c7746da157308dfc71862c2bdb7b855c4348d4d377380f3a48237d3e9a"},
	"update_finding_report":        {"243d0fbc8e96f7bb76ac8e1e766baa1fec0c61c458d0689bcc50b5624bdeca9f", "0cdf7cf2e4bc901f8734a01a982298f41f16b1f31be3842217525aa608972508"},
	"update_mcp":                   {"f045b5bd141083f8837c0958af8fdcce92b120a164b407a62d77063926a61f10", "3886ac763c77090161980af5ca0cfb02e500169bea33c06cc40c7587e46343b4"},
	"update_skill_file":            {"c9c09017e8049d928603dbbfbfc66078d64ef5889368d4f96eb954d86fb6d994", "22bf9272dd59ffa5416a1c6be69b6f5ba512ed7de424f6cee714af209a49d0ae"},
}

// JSONB는 키 순서·공백을 바꾸므로 JSON 값으로 읽은 뒤 정규화한다.
func legacySchemaHash(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return legacyTextHash(string(normalized)), nil
}

// traffic_search의 더 이전 기본값도 기존 정확 대조 계약에 포함한다.
const legacyTrafficSearchDescriptionHash = "5adfaf6a43f0c3d34d3b9d9994394f6dc2e3174d4789600d1b5abe44fa21617c"

func isLegacyToolDescription(key, value string) bool {
	entry, ok := legacyToolHashes[key]
	if !ok {
		return false
	}
	got := legacyTextHash(value)
	return got == entry.Description || (key == "traffic_search" && got == legacyTrafficSearchDescriptionHash)
}
