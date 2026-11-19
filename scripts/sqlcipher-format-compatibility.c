#include <sqlite3.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void require(int ok, sqlite3 *db, const char *step) {
    if (!ok) {
        fprintf(stderr, "SQLCipher compatibility failure: %s: %s\n", step,
                db ? sqlite3_errmsg(db) : "no database");
        exit(1);
    }
}

static void execute(sqlite3 *db, const char *sql) {
    require(sqlite3_exec(db, sql, NULL, NULL, NULL) == SQLITE_OK, db, "execute fixture SQL");
}

int main(int argc, char **argv) {
    if (argc != 4 || (strcmp(argv[1], "create") && strcmp(argv[1], "rewrite") && strcmp(argv[1], "read"))) {
        fprintf(stderr, "usage: probe create|rewrite|read private-db expected-version\n");
        return 1;
    }
    sqlite3 *db = NULL;
    sqlite3_stmt *statement = NULL;
    unsigned char key[32];
    for (size_t i = 0; i < sizeof key; i++) key[i] = (unsigned char)(i + 1);
    int flags = !strcmp(argv[1], "create") ? SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE : SQLITE_OPEN_READWRITE;
    require(sqlite3_open_v2(argv[2], &db, flags, NULL) == SQLITE_OK, db, "open private fixture");
    require(sqlite3_key(db, key, (int)sizeof key) == SQLITE_OK, db, "set fixture key");
    require(sqlite3_prepare_v2(db, "PRAGMA cipher_version", -1, &statement, NULL) == SQLITE_OK, db, "read library version");
    require(sqlite3_step(statement) == SQLITE_ROW, db, "SQLCipher is required");
    const char *version = (const char *)sqlite3_column_text(statement, 0);
    size_t length = strlen(argv[3]);
    require(version && !strncmp(version, argv[3], length) &&
            (version[length] == '\0' || version[length] == ' '), db, "unexpected library version");
    require(sqlite3_finalize(statement) == SQLITE_OK, db, "finalize version query");
    if (!strcmp(argv[1], "create")) {
        execute(db, "CREATE TABLE compatibility_probe(item TEXT NOT NULL, amount INTEGER NOT NULL)");
        execute(db, "INSERT INTO compatibility_probe VALUES('ci-format-probe', 1234)");
    } else {
        require(sqlite3_prepare_v2(db, "SELECT item, amount FROM compatibility_probe", -1, &statement, NULL) == SQLITE_OK, db, "read old encrypted schema");
        require(sqlite3_step(statement) == SQLITE_ROW, db, "read saved row");
        const char *item = (const char *)sqlite3_column_text(statement, 0);
        require(item && !strcmp(item, "ci-format-probe"), db, "saved text differs");
        int expected = !strcmp(argv[1], "rewrite") ? 1234 : 2345;
        require(sqlite3_column_int64(statement, 1) == expected, db, "saved amount differs");
        require(sqlite3_step(statement) == SQLITE_DONE, db, "unexpected extra fixture rows");
        require(sqlite3_finalize(statement) == SQLITE_OK, db, "finalize fixture query");
        if (!strcmp(argv[1], "rewrite")) execute(db, "UPDATE compatibility_probe SET amount=2345");
    }
    require(sqlite3_close(db) == SQLITE_OK, NULL, "close private fixture");
    return 0;
}
