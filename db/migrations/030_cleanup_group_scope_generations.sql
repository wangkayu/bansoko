-- Forward-only migration 030: local-data cleanup of a retired anime failed on
-- provider_group_scope_generations, a leaf row per subscription and provider
-- that cleanup_managed_anime_local_data never deleted, so deleting the
-- subscription violated its foreign key. The row only records the subscription's
-- group-scope generation and has no dependents, so it goes with its
-- subscription.

ALTER TABLE provider_group_scope_generations
    DROP CONSTRAINT provider_group_scope_generations_subscription_id_fkey,
    ADD CONSTRAINT provider_group_scope_generations_subscription_id_fkey
        FOREIGN KEY (subscription_id) REFERENCES subscriptions (id) ON DELETE CASCADE;
