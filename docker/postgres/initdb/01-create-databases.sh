#!/bin/sh
# Creates auxiliary databases used by tooling:
#   - ecommerce_dev:  Atlas dev database used to replay/diff migrations (must stay empty between runs)
#   - ecommerce_test: integration-test database
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE ecommerce_dev;
    CREATE DATABASE ecommerce_test;
EOSQL
