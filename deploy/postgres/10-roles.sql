-- Local development only: login roles for docker-compose. The migrations create these
-- roles as NOLOGIN when they are missing; production sets passwords out of band.
CREATE ROLE tollgate_app LOGIN PASSWORD 'tollgate_app_dev' NOSUPERUSER NOBYPASSRLS;
CREATE ROLE tollgate_publisher LOGIN PASSWORD 'tollgate_publisher_dev' NOSUPERUSER NOBYPASSRLS;
