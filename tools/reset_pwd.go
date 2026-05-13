package main

import (
	"fmt"
	"os"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run reset_pwd.go <你的新密码>")
		return
	}

	newPassword := os.Args[1]

	// 使用 bcrypt 算法对新密码进行哈希加密
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		fmt.Printf("生成密码失败: %v\n", err)
		return
	}

	fmt.Println("\n=== 密码重置工具 ===")
	fmt.Printf("你输入的新明文密码是: %s\n", newPassword)
	fmt.Println("\n请打开 Navicat，找到对应的用户，将他的 password 字段修改为以下这串乱码：")
	fmt.Println("--------------------------------------------------")
	fmt.Println(string(hashedPassword))
	fmt.Println("--------------------------------------------------")
	fmt.Println("修改后，你就可以用这个新密码在前端登录了！")
}
