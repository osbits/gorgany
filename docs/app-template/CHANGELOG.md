# Changelog

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

Every release that needs an operator to do something has a **Deployment notes**
subsection: migrations to run, environment variables to add, manual steps, and whether
the release can be rolled back without restoring a backup.

## [Unreleased]

### Added

- Notes API: `GET /api/v1/notes`, `GET /api/v1/notes/{id}`, `POST /api/v1/notes`.

### Deployment notes

- New migration `20260926_120000.create_notes_and_users`, and a new reference seeder
  `welcome_note`. The deploy runs `backup`, `migrate` and `seed` before the new version
  starts (docs/runbooks/deploy.md). Rolling back needs no restore.
