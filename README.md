# Installation

`rbench` will use aws sdk to run golang package benchmarks in a temporary ec2 instance. Instance type is configurable (flag).

```
aws configure // install aws-cli
go install github.com/gbotrel/rbench@latest
```


## Usage

```
cd my_go_package
rbench // reasonable following default flags:
rbench -type=t2.micro -run=NONE -bench=. -benchmem -count=5 | tee bench.txt
```
