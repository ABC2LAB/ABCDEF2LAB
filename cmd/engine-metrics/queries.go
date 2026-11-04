package main

// This CLI supports the repository's public schema only. No schema discovery,
// caller-provided SQL, payload extraction, or writes belong in this tool.
const scopeSQL = `SELECT e.id,t.id,pg_catalog.transaction_timestamp()
FROM public.explorations e LEFT JOIN public.tasks t ON t.exploration_id=e.id
WHERE ($1::bigint>0 AND t.id=$1) OR ($2::bigint>0 AND e.id=$2)`

const intentsSQL = `WITH scoped AS (
 SELECT state,created_at,completed_at,
 CASE WHEN created_at IS NOT NULL AND completed_at IS NOT NULL
 AND pg_catalog.isfinite(created_at) AND pg_catalog.isfinite(completed_at)
 AND completed_at>=created_at
 THEN EXTRACT(EPOCH FROM (completed_at-created_at))::double precision END AS elapsed
 FROM public.exploration_nodes WHERE exploration_id=$1 AND kind='intent'
)
SELECT count(*),
 count(*) FILTER(WHERE state='open'),count(*) FILTER(WHERE state='running'),
 count(*) FILTER(WHERE state='paused'),count(*) FILTER(WHERE state='done'),
 count(*) FILTER(WHERE state='blocked'),count(*) FILTER(WHERE state='exhausted'),
 count(*) FILTER(WHERE state='stopped'),count(*) FILTER(WHERE state='deleted'),
 count(*) FILTER(WHERE state NOT IN ('open','running','paused','done','blocked','exhausted','stopped','deleted')),
 count(*) FILTER(WHERE state IS NULL),
 count(*) FILTER(WHERE created_at IS NULL),count(*) FILTER(WHERE completed_at IS NULL),
 count(*) FILTER(WHERE state IN ('done','blocked','exhausted','stopped') AND completed_at IS NULL),
 count(*) FILTER(WHERE NOT pg_catalog.isfinite(created_at) OR NOT pg_catalog.isfinite(completed_at)),
 count(*) FILTER(WHERE pg_catalog.isfinite(created_at) AND pg_catalog.isfinite(completed_at) AND completed_at<created_at),
 count(elapsed),min(elapsed),max(elapsed),avg(elapsed),sum(elapsed) FROM scoped`

const tokenAggregates = `count(*), count(*) FILTER(WHERE worker='planner' AND kind='round'),
 sum(input_tokens), count(*) FILTER(WHERE input_tokens IS NULL), count(*) FILTER(WHERE input_tokens<0),
 sum(output_tokens), count(*) FILTER(WHERE output_tokens IS NULL), count(*) FILTER(WHERE output_tokens<0),
 sum(cache_read_tokens), count(*) FILTER(WHERE cache_read_tokens IS NULL), count(*) FILTER(WHERE cache_read_tokens<0),
 sum(cache_write_tokens), count(*) FILTER(WHERE cache_write_tokens IS NULL), count(*) FILTER(WHERE cache_write_tokens<0)`

const activitySQL = `WITH scoped AS (
 SELECT worker,kind,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens
 FROM public.activity WHERE exploration_id=$1
)
SELECT 0 AS bucket,` + tokenAggregates + ` FROM scoped
UNION ALL SELECT 1 AS bucket,` + tokenAggregates + ` FROM scoped WHERE kind='result'
ORDER BY bucket`
