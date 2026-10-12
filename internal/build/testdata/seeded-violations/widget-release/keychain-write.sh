#!/usr/bin/env bash
# Seeded violation: a keychain write the widget release scripts must never contain.
security import fake.p12 -k login.keychain-db
security list-keychains -s fake.keychain-db
