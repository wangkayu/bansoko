CREATE TABLE public.provider_outbound_health (
    bucket_key text PRIMARY KEY CHECK (bucket_key ~ '^[a-z0-9_]+:[a-z0-9_]+$' AND length(bucket_key) <= 128),
    cooldown_until timestamptz NOT NULL CHECK (isfinite(cooldown_until)),
    last_limited_at timestamptz NOT NULL,
    reason_code text NOT NULL CHECK (length(reason_code) BETWEEN 1 AND 128),
    summary text NOT NULL CHECK (length(summary) BETWEEN 1 AND 512),
    consecutive integer NOT NULL CHECK (consecutive BETWEEN 1 AND 7),
    version bigint NOT NULL CHECK (version >= 1),
    updated_at timestamptz NOT NULL
);
REVOKE ALL ON TABLE public.provider_outbound_health FROM PUBLIC;
REVOKE ALL ON TABLE public.provider_outbound_health FROM bansoko_runtime;
GRANT SELECT, INSERT, UPDATE ON TABLE public.provider_outbound_health TO bansoko_runtime;
