-- Optional teammate-visible display name (design-history follow-up). Pure addition, nullable, no default.
-- modify "users" table
ALTER TABLE "users" ADD COLUMN "display_name" character varying(80) NULL;
