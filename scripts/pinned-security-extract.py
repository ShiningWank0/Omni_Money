"""Read-only scan inputs: unpack regular files, never execute their payloads."""
import pathlib
import shutil
import sys
import tarfile


def unpack(archive, destination):
    base = pathlib.Path(destination).resolve()
    base.mkdir(mode=0o700, parents=True, exist_ok=False)
    total = 0
    with tarfile.open(archive, "r:gz") as source:
        for index, member in enumerate(source):
            path = pathlib.PurePosixPath(member.name)
            if index >= 50000 or path.is_absolute() or ".." in path.parts:
                raise ValueError("unsafe archive path or excessive entry count")
            if not member.isfile() and not member.isdir():
                raise ValueError("archive links and special files are forbidden")
            target = base.joinpath(*path.parts)
            if member.isdir():
                target.mkdir(mode=0o700, parents=True, exist_ok=True)
                continue
            total += member.size
            if member.size > 512 * 1024**2 or total > 2 * 1024**3:
                raise ValueError("archive exceeds scan size limit")
            target.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            with source.extractfile(member) as reader, target.open("xb") as writer:
                shutil.copyfileobj(reader, writer)
            with target.open("rb") as reader:
                magic = reader.read(4)
            # Static Go analyzers inspect executable files; setting the mode
            # does not execute any downloaded product binary.
            target.chmod(0o700 if magic.startswith((b"\x7fELF", b"MZ")) else 0o600)
    if not total:
        raise ValueError("empty scan archive")


if __name__ == "__main__":
    unpack(sys.argv[1], sys.argv[2])
